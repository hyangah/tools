// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tool_test

import (
	"bytes"
	"context"
	"flag"
	"strings"
	"testing"

	"golang.org/x/tools/internal/tool"
)

// testParent is an Application with global-style flags, used as the
// inherited parent in RunInherited tests. It embeds tool.Profile to
// exercise the profile-inheritance path.
type testParent struct {
	tool.Profile

	Verbose bool   `flag:"v,verbose" help:"verbose output"`
	Remote  string `flag:"remote" help:"remote address"`

	// ranWith records the positional args the last Run call received.
	ranWith []string
}

func (p *testParent) Name() string                 { return "parent" }
func (p *testParent) Usage() string                { return "" }
func (p *testParent) ShortHelp() string            { return "parent short help" }
func (p *testParent) DetailedHelp(f *flag.FlagSet) { f.PrintDefaults() }
func (p *testParent) Run(_ context.Context, args ...string) error {
	p.ranWith = append([]string(nil), args...)
	return nil
}

// testSub is a subcommand Application with its own flag, no overlap
// with testParent.
type testSub struct {
	JSON bool `flag:"json" help:"emit json"`

	ran bool
}

func (*testSub) Name() string                   { return "sub" }
func (*testSub) Usage() string                  { return "" }
func (*testSub) ShortHelp() string              { return "sub short help" }
func (s *testSub) DetailedHelp(f *flag.FlagSet) { f.PrintDefaults() }
func (s *testSub) Run(_ context.Context, _ ...string) error {
	s.ran = true
	return nil
}

// testSubShadow declares a flag named "v" that collides with testParent's
// "v,verbose" group; the collision must resolve in favor of the subcommand.
type testSubShadow struct {
	V bool `flag:"v" help:"subcommand v"`
}

func (*testSubShadow) Name() string                             { return "shadow" }
func (*testSubShadow) Usage() string                            { return "" }
func (*testSubShadow) ShortHelp() string                        { return "shadow" }
func (*testSubShadow) DetailedHelp(f *flag.FlagSet)             { f.PrintDefaults() }
func (*testSubShadow) Run(_ context.Context, _ ...string) error { return nil }

func TestRun_baseline(t *testing.T) {
	parent := &testParent{}
	s := flag.NewFlagSet("parent", flag.ContinueOnError)
	s.SetOutput(&bytes.Buffer{})
	if err := tool.Run(context.Background(), s, parent, []string{"-v", "arg1"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !parent.Verbose {
		t.Error("-v did not set Verbose")
	}
	if got, want := parent.ranWith, []string{"arg1"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("ranWith = %v, want %v", got, want)
	}
}

func TestRunInherited_parentFlagAfterSub(t *testing.T) {
	parent := &testParent{}
	sub := &testSub{}
	s := flag.NewFlagSet("sub", flag.ContinueOnError)
	s.SetOutput(&bytes.Buffer{})
	if err := tool.RunInherited(context.Background(), s, sub, parent, []string{"-v", "-json"}); err != nil {
		t.Fatalf("RunInherited: %v", err)
	}
	if !parent.Verbose {
		t.Error("-v did not set parent.Verbose through inheritance")
	}
	if !sub.JSON {
		t.Error("-json did not set sub.JSON")
	}
	if !sub.ran {
		t.Error("sub.Run was not invoked")
	}
}

func TestRunInherited_parentStringFlag(t *testing.T) {
	parent := &testParent{}
	sub := &testSub{}
	s := flag.NewFlagSet("sub", flag.ContinueOnError)
	s.SetOutput(&bytes.Buffer{})
	if err := tool.RunInherited(context.Background(), s, sub, parent, []string{"-remote=localhost:1234"}); err != nil {
		t.Fatalf("RunInherited: %v", err)
	}
	if got, want := parent.Remote, "localhost:1234"; got != want {
		t.Errorf("parent.Remote = %q, want %q", got, want)
	}
}

func TestRunInherited_subcommandWinsOnCollision(t *testing.T) {
	parent := &testParent{}
	sub := &testSubShadow{}
	s := flag.NewFlagSet("shadow", flag.ContinueOnError)
	s.SetOutput(&bytes.Buffer{})
	if err := tool.RunInherited(context.Background(), s, sub, parent, []string{"-v"}); err != nil {
		t.Fatalf("RunInherited: %v", err)
	}
	if !sub.V {
		t.Error("-v did not set sub.V")
	}
	if parent.Verbose {
		t.Error("parent.Verbose was set; collision rule should have prevented inheritance")
	}
	// The whole "-v,verbose" group should have been skipped, so the alias
	// is not registered either.
	if f := s.Lookup("verbose"); f != nil {
		t.Errorf("unexpected -verbose flag registered: %v", f)
	}
}

func TestRunInherited_helpListsBothFlagSets(t *testing.T) {
	parent := &testParent{}
	sub := &testSub{}
	var buf bytes.Buffer
	s := flag.NewFlagSet("sub", flag.ContinueOnError)
	s.SetOutput(&buf)
	// -h triggers Usage and returns flag.ErrHelp; ignore the error.
	_ = tool.RunInherited(context.Background(), s, sub, parent, []string{"-h"})
	out := buf.String()
	for _, want := range []string{"-json", "-v", "-verbose", "-remote"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %q:\n%s", want, out)
		}
	}
}

func TestRunInherited_profileInheritedFromParent(t *testing.T) {
	parent := &testParent{}
	sub := &testSub{}
	s := flag.NewFlagSet("sub", flag.ContinueOnError)
	s.SetOutput(&bytes.Buffer{})
	// Use -h so Parse returns without needing real positional args or
	// actually invoking profilers.
	_ = tool.RunInherited(context.Background(), s, sub, parent, []string{"-h"})
	for _, name := range []string{"profile.cpu", "profile.mem", "profile.alloc", "profile.trace", "profile.block"} {
		if s.Lookup(name) == nil {
			t.Errorf("profile flag %q not registered on subcommand FlagSet", name)
		}
	}
}

func TestRunInherited_nilParentMatchesRun(t *testing.T) {
	// Passing nil parent should behave identically to tool.Run.
	parent := &testParent{}
	s := flag.NewFlagSet("parent", flag.ContinueOnError)
	s.SetOutput(&bytes.Buffer{})
	if err := tool.RunInherited(context.Background(), s, parent, nil, []string{"-v"}); err != nil {
		t.Fatalf("RunInherited: %v", err)
	}
	if !parent.Verbose {
		t.Error("-v did not set Verbose")
	}
}
