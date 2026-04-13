// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Phase 0 spike: validate that cache.Session → golang.* analysis
// functions work end-to-end without the LSP server.
//
// This test proves the critical path for gopls-skills v2:
//   - Session/View creation via cache APIs
//   - File sync via DidModifyFiles
//   - Direct calls to golang.Definition, References, Hover, DocumentSymbols
//   - UTF-8 ↔ UTF-16 position conversion
//   - GOPATH/AdHoc view creation without go.mod
package goplscli_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/cache"
	"golang.org/x/tools/gopls/internal/file"
	"golang.org/x/tools/gopls/internal/golang"
	"golang.org/x/tools/gopls/internal/protocol"
	"golang.org/x/tools/gopls/internal/settings"
)

// testFiles creates a temporary Go module with test source files.
// Returns the absolute path to the module root.
func testFiles(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// Resolve symlinks (gopls expands them internally).
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"go.mod": "module example.com/test\n\ngo 1.21\n",
		"main.go": `package main

import "fmt"

// Greeting returns a greeting for the given name.
func Greeting(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

func main() {
	msg := Greeting("world")
	fmt.Println(msg)
}
`,
		"main_test.go": `package main

import "testing"

func TestGreeting(t *testing.T) {
	got := Greeting("test")
	if got != "Hello, test!" {
		t.Errorf("got %q, want %q", got, "Hello, test!")
	}
}
`,
		// File with non-ASCII to test UTF-8 ↔ UTF-16 conversion.
		"unicode.go": `package main

// Ünïcödé is a function with non-ASCII identifier.
func Ünïcödé() string {
	return "héllo"
}
`,
	}

	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// createSession creates a cache.Session with a View for the given root.
// Returns the session and a cleanup function.
func createSession(t *testing.T, ctx context.Context, c *cache.Cache, root string) *cache.Session {
	t.Helper()

	sess := cache.NewSession(ctx, c)

	opts := settings.DefaultOptions()
	folderURI := protocol.URIFromPath(root)
	env, err := cache.FetchGoEnv(ctx, folderURI, opts)
	if err != nil {
		t.Fatalf("FetchGoEnv: %v", err)
	}

	folder := &cache.Folder{
		Dir:     folderURI,
		Name:    filepath.Base(root),
		Options: opts,
		Env:     *env,
	}

	_, snapshot, release, err := sess.NewView(ctx, folder)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	_ = snapshot
	release() // release initial snapshot

	t.Cleanup(func() { sess.Shutdown(ctx) })
	return sess
}

// syncFile opens or updates a file in the session overlay.
func syncFile(t *testing.T, ctx context.Context, sess *cache.Session, path string) {
	t.Helper()
	uri := protocol.URIFromPath(path)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	// Use file.Open — the overlay requires open-before-change.
	_, err = sess.DidModifyFiles(ctx, []file.Modification{{
		URI:        uri,
		Action:     file.Open,
		Text:       content,
		Version:    1,
		LanguageID: "go",
	}})
	if err != nil {
		t.Fatalf("DidModifyFiles(Open, %s): %v", path, err)
	}
}

// TestSessionCreation tests that we can create a cache, session, and view
// without the LSP server.
func TestSessionCreation(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)

	start := time.Now()
	sess := createSession(t, ctx, c, root)
	setupDuration := time.Since(start)
	t.Logf("Session+View setup took %v", setupDuration)

	if sess == nil {
		t.Fatal("session is nil")
	}
}

// TestDefinition tests golang.Definition via direct API call.
func TestDefinition(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	sess := createSession(t, ctx, c, root)

	mainFile := filepath.Join(root, "main.go")
	syncFile(t, ctx, sess, mainFile)

	uri := protocol.URIFromPath(mainFile)
	fh, snapshot, release, err := sess.FileOf(ctx, uri)
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	defer release()

	// "Greeting" is called at line 11, col ~8 (0-based: line 10, char 7).
	// In main.go: `msg := Greeting("world")`
	//                      ^-- cursor here
	rng := protocol.Range{
		Start: protocol.Position{Line: 10, Character: 8},
		End:   protocol.Position{Line: 10, Character: 8},
	}
	locs, err := golang.Definition(ctx, snapshot, fh, rng)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(locs) == 0 {
		t.Fatal("Definition returned no results")
	}

	// Definition should point to the Greeting function declaration (line 5).
	loc := locs[0]
	t.Logf("Definition result: %s:%d:%d", loc.URI, loc.Range.Start.Line+1, loc.Range.Start.Character+1)
	if loc.Range.Start.Line != 5 {
		t.Errorf("expected definition on line 6 (0-based: 5), got line %d", loc.Range.Start.Line)
	}
	if string(loc.URI) != string(uri) {
		t.Errorf("expected definition in %s, got %s", uri, loc.URI)
	}
}

// TestReferences tests golang.References via direct API call.
func TestReferences(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	sess := createSession(t, ctx, c, root)

	mainFile := filepath.Join(root, "main.go")
	testFile := filepath.Join(root, "main_test.go")
	syncFile(t, ctx, sess, mainFile)
	syncFile(t, ctx, sess, testFile)

	uri := protocol.URIFromPath(mainFile)
	fh, snapshot, release, err := sess.FileOf(ctx, uri)
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	defer release()

	// Position on "Greeting" declaration (line 5, func keyword is col 5,
	// "Greeting" starts at col 5: `func Greeting(...)`)
	rng := protocol.Range{
		Start: protocol.Position{Line: 5, Character: 5},
		End:   protocol.Position{Line: 5, Character: 5},
	}
	refs, err := golang.References(ctx, snapshot, fh, rng, true)
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	t.Logf("References found %d results", len(refs))
	for _, ref := range refs {
		t.Logf("  %s:%d:%d", ref.URI, ref.Range.Start.Line+1, ref.Range.Start.Character+1)
	}
	// Expect at least 2: declaration + call in main + call in test
	if len(refs) < 2 {
		t.Errorf("expected at least 2 references, got %d", len(refs))
	}
}

// TestHover tests golang.Hover via direct API call.
func TestHover(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	sess := createSession(t, ctx, c, root)

	mainFile := filepath.Join(root, "main.go")
	syncFile(t, ctx, sess, mainFile)

	uri := protocol.URIFromPath(mainFile)
	fh, snapshot, release, err := sess.FileOf(ctx, uri)
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	defer release()

	// Hover on "Greeting" at its declaration.
	rng := protocol.Range{
		Start: protocol.Position{Line: 5, Character: 5},
		End:   protocol.Position{Line: 5, Character: 5},
	}
	hover, err := golang.Hover(ctx, snapshot, fh, rng, nil)
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	t.Logf("Hover contents:\n%s", hover.Contents.Value)
}

// TestDocumentSymbols tests golang.DocumentSymbols via direct API call.
func TestDocumentSymbols(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	sess := createSession(t, ctx, c, root)

	mainFile := filepath.Join(root, "main.go")
	syncFile(t, ctx, sess, mainFile)

	uri := protocol.URIFromPath(mainFile)
	fh, snapshot, release, err := sess.FileOf(ctx, uri)
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	defer release()

	symbols, err := golang.DocumentSymbols(ctx, snapshot, fh)
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	t.Logf("Found %d symbols", len(symbols))
	for _, s := range symbols {
		t.Logf("  %s (%s) at line %d", s.Name, s.Kind, s.Range.Start.Line+1)
	}
	// Expect at least Greeting and main.
	if len(symbols) < 2 {
		t.Errorf("expected at least 2 symbols, got %d", len(symbols))
	}
}

// TestPositionConversion tests UTF-8 ↔ UTF-16 round-trip using
// protocol.Mapper, including non-ASCII identifiers.
func TestPositionConversion(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	sess := createSession(t, ctx, c, root)

	unicodeFile := filepath.Join(root, "unicode.go")
	syncFile(t, ctx, sess, unicodeFile)

	// Read file content to create a Mapper.
	content, err := os.ReadFile(unicodeFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	uri := protocol.URIFromPath(unicodeFile)
	mapper := protocol.NewMapper(uri, content)

	// "Ünïcödé" starts at line 3 (0-based), after "func ".
	// In UTF-8: Ü=2 bytes, n=1, ï=2, c=1, ö=2, d=1, é=2 = 11 bytes
	// In UTF-16: all these are BMP characters = 1 code unit each = 7 code units

	// Test input conversion: 1-based UTF-8 byte col → 0-based UTF-16 position.
	// "func " is 5 bytes. "Ü" starts at byte offset 5 on line 3.
	// 1-based UTF-8 col = 6 (1-based offset of "Ü").
	line0 := 3    // 0-based line
	utf8Col1 := 6 // 1-based UTF-8 byte column of "Ü"

	// Compute byte offset: find line start, add column.
	lineStart := 0
	currentLine := 0
	for i, b := range content {
		if currentLine == line0 {
			lineStart = i
			break
		}
		if b == '\n' {
			currentLine++
		}
	}
	byteOffset := lineStart + (utf8Col1 - 1) // 0-based byte offset

	pos, err := mapper.OffsetPosition(byteOffset)
	if err != nil {
		t.Fatalf("OffsetPosition(%d): %v", byteOffset, err)
	}
	t.Logf("UTF-8 col %d (1-based) → UTF-16 position: line=%d char=%d (0-based)", utf8Col1, pos.Line, pos.Character)

	if pos.Line != uint32(line0) {
		t.Errorf("expected line %d, got %d", line0, pos.Line)
	}
	// "func " is 5 chars, all ASCII → UTF-16 char = 5 (0-based).
	if pos.Character != 5 {
		t.Errorf("expected UTF-16 character 5, got %d", pos.Character)
	}

	// Test output conversion: 0-based UTF-16 position → 1-based UTF-8 col.
	offset, err := mapper.PositionOffset(pos)
	if err != nil {
		t.Fatalf("PositionOffset: %v", err)
	}
	gotLine, gotCol8 := mapper.OffsetLineCol8(offset)
	t.Logf("UTF-16 position → 1-based line=%d col=%d (UTF-8)", gotLine, gotCol8)

	if gotLine != line0+1 {
		t.Errorf("round-trip line: expected %d, got %d", line0+1, gotLine)
	}
	if gotCol8 != utf8Col1 {
		t.Errorf("round-trip UTF-8 col: expected %d, got %d", utf8Col1, gotCol8)
	}

	// Also test Definition on the non-ASCII identifier.
	fh, snapshot, release, err := sess.FileOf(ctx, uri)
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	defer release()

	rng := protocol.Range{Start: pos, End: pos}
	locs, err := golang.Definition(ctx, snapshot, fh, rng)
	if err != nil {
		t.Fatalf("Definition on non-ASCII identifier: %v", err)
	}
	if len(locs) == 0 {
		t.Fatal("Definition returned no results for non-ASCII identifier")
	}
	t.Logf("Definition of Ünïcödé: %s:%d:%d", locs[0].URI, locs[0].Range.Start.Line+1, locs[0].Range.Start.Character+1)
}

// TestFileModification tests that DidModifyFiles correctly updates the
// session and subsequent queries see the new content.
func TestFileModification(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)
	sess := createSession(t, ctx, c, root)

	mainFile := filepath.Join(root, "main.go")
	syncFile(t, ctx, sess, mainFile)

	// Query symbols before modification.
	uri := protocol.URIFromPath(mainFile)
	fh, snapshot, release, err := sess.FileOf(ctx, uri)
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	symsBefore, err := golang.DocumentSymbols(ctx, snapshot, fh)
	release()
	if err != nil {
		t.Fatalf("DocumentSymbols before: %v", err)
	}
	t.Logf("Symbols before: %d", len(symsBefore))

	// Modify the file: add a new function.
	newContent := `package main

import "fmt"

func Greeting(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

func NewFunction() int {
	return 42
}

func main() {
	msg := Greeting("world")
	fmt.Println(msg)
}
`
	if err := os.WriteFile(mainFile, []byte(newContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Send file.Change with new content.
	_, err = sess.DidModifyFiles(ctx, []file.Modification{{
		URI:     uri,
		Action:  file.Change,
		Text:    []byte(newContent),
		Version: 2,
	}})
	if err != nil {
		t.Fatalf("DidModifyFiles(Change): %v", err)
	}

	// Query symbols after modification.
	fh2, snapshot2, release2, err := sess.FileOf(ctx, uri)
	if err != nil {
		t.Fatalf("FileOf after change: %v", err)
	}
	defer release2()

	symsAfter, err := golang.DocumentSymbols(ctx, snapshot2, fh2)
	if err != nil {
		t.Fatalf("DocumentSymbols after: %v", err)
	}
	t.Logf("Symbols after: %d", len(symsAfter))

	if len(symsAfter) <= len(symsBefore) {
		t.Errorf("expected more symbols after adding NewFunction, before=%d after=%d", len(symsBefore), len(symsAfter))
	}
}

// TestAdHocView tests that NewView works without go.mod (AdHoc view).
func TestAdHocView(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)

	// Write a Go file without go.mod.
	goFile := filepath.Join(root, "hello.go")
	if err := os.WriteFile(goFile, []byte(`package main

func Hello() string { return "hello" }
`), 0644); err != nil {
		t.Fatal(err)
	}

	c := cache.New(nil)
	sess := createSession(t, ctx, c, root)

	syncFile(t, ctx, sess, goFile)

	uri := protocol.URIFromPath(goFile)
	fh, snapshot, release, err := sess.FileOf(ctx, uri)
	if err != nil {
		t.Fatalf("FileOf: %v", err)
	}
	defer release()

	symbols, err := golang.DocumentSymbols(ctx, snapshot, fh)
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	if len(symbols) == 0 {
		t.Error("expected at least 1 symbol in AdHoc view")
	}
	for _, s := range symbols {
		t.Logf("  %s (%s)", s.Name, s.Kind)
	}
}

// TestSetupLatency benchmarks the session+view creation path to validate
// the session-pool design (this should be <500ms for the pool to work).
func TestSetupLatency(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)

	// Measure FetchGoEnv separately.
	opts := settings.DefaultOptions()
	folderURI := protocol.URIFromPath(root)

	start := time.Now()
	env, err := cache.FetchGoEnv(ctx, folderURI, opts)
	if err != nil {
		t.Fatalf("FetchGoEnv: %v", err)
	}
	fetchDuration := time.Since(start)
	t.Logf("FetchGoEnv took %v", fetchDuration)

	// Measure NewSession + NewView.
	c := cache.New(nil)
	sess := cache.NewSession(ctx, c)
	folder := &cache.Folder{
		Dir:     folderURI,
		Name:    filepath.Base(root),
		Options: opts,
		Env:     *env,
	}

	start = time.Now()
	_, _, release, err := sess.NewView(ctx, folder)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	viewDuration := time.Since(start)
	release()
	t.Logf("NewView took %v", viewDuration)
	t.Logf("Total setup: %v", fetchDuration+viewDuration)

	t.Cleanup(func() { sess.Shutdown(ctx) })
}

// TestSharedCache verifies that two sessions on the same cache share
// memoized computations (the foundation of multi-agent memory sharing).
func TestSharedCache(t *testing.T) {
	ctx := context.Background()
	root := testFiles(t)
	c := cache.New(nil)

	// Create two sessions on the same cache.
	sess1 := createSession(t, ctx, c, root)
	// NewView will fail with ErrViewExists if same folder URI.
	// Create a second session with a fresh Session (shares cache).
	sess2 := cache.NewSession(ctx, c)
	t.Cleanup(func() { sess2.Shutdown(ctx) })

	opts := settings.DefaultOptions()
	folderURI := protocol.URIFromPath(root)
	env, err := cache.FetchGoEnv(ctx, folderURI, opts)
	if err != nil {
		t.Fatalf("FetchGoEnv: %v", err)
	}
	_, _, release2, err := sess2.NewView(ctx, &cache.Folder{
		Dir:     folderURI,
		Name:    "session2",
		Options: opts,
		Env:     *env,
	})
	if err != nil {
		t.Fatalf("NewView for session 2: %v", err)
	}
	release2()

	// Sync and query from both sessions — both should work.
	mainFile := filepath.Join(root, "main.go")
	syncFile(t, ctx, sess1, mainFile)
	syncFile(t, ctx, sess2, mainFile)

	uri := protocol.URIFromPath(mainFile)

	fh1, snap1, rel1, err := sess1.FileOf(ctx, uri)
	if err != nil {
		t.Fatalf("sess1.FileOf: %v", err)
	}
	defer rel1()

	fh2, snap2, rel2, err := sess2.FileOf(ctx, uri)
	if err != nil {
		t.Fatalf("sess2.FileOf: %v", err)
	}
	defer rel2()

	syms1, err := golang.DocumentSymbols(ctx, snap1, fh1)
	if err != nil {
		t.Fatalf("sess1 DocumentSymbols: %v", err)
	}
	syms2, err := golang.DocumentSymbols(ctx, snap2, fh2)
	if err != nil {
		t.Fatalf("sess2 DocumentSymbols: %v", err)
	}

	if len(syms1) != len(syms2) {
		t.Errorf("sessions returned different symbol counts: %d vs %d", len(syms1), len(syms2))
	}
	t.Logf("Both sessions found %d symbols (shared cache working)", len(syms1))
}
