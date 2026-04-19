---
title: "gopls CLI target reference"
---

## Status

**Draft.** Sample help text companion to
[gopls-cli-migration.md](gopls-cli-migration.md). Captures the
target content and shape of `gopls help` and `gopls <cmd> --help`
output after the migration's Phase 1 lands. The help-text generator
that produces output this terse is a separate workstream; today's
`gopls help` is significantly more verbose. This file is not a
commitment to any particular rendering mechanism.

## Top-level help

```
gopls — Go language server and CLI

usage: gopls <command> [args]

navigation
  def SYMBOL --in FILE         show where SYMBOL is defined
  refs SYMBOL --in FILE        find references to SYMBOL
  impl SYMBOL --in FILE        find implementations of SYMBOL
  hover SYMBOL --in FILE       show SYMBOL's signature and doc
  symbols FILE                 list top-level symbols in FILE
  wsymbols QUERY               search workspace symbols
  callers SYMBOL --in FILE     list callers of SYMBOL
  callees SYMBOL --in FILE     list functions SYMBOL calls

diagnostics
  check [FILE...] [--fix]      report diagnostics; --fix applies quickfixes
  vet [FILE...] [--fix]        cmd/vet-class diagnostics; --fix applies quickfixes

edits and refactoring
  rename SYMBOL --in FILE --to NEW  rename a symbol
  format FILE...               apply gofmt
  imports FILE...              organize imports
  codeaction POS [--kind KIND] list or apply a code action at POS

server
  serve                        run as an LSP server
  mcp                          run as an MCP server

other
  version, help, bug, stats, remote, vulncheck

global flags
  --json                       emit JSON instead of text
  -v, --verbose                verbose stderr
  --remote=auto                connect to daemon (default)

run `gopls help <command>` for command details.
```

## Shared conventions

**Position** (any command taking `POS` or `SYMBOL --in FILE`):

- `SYMBOL --in FILE` — preferred; agents know names.
- `SYMBOL --in FILE:LINE` — disambiguate by line hint.
- `FILE:LINE:COL` — for parsing compiler output.

Lines and columns are 1-based UTF-8 bytes. Out-of-range coordinates
clamp to the nearest valid position.

**Edit flags** (apply to `rename`, `format`, `imports`, `codeaction`,
and `check`/`vet` with `--fix`):

```
(default) print edited content to stdout
-w        write in place (only if changed)
-d        print unified diff
-l        print filenames that would change
--preserve  with -w, write .orig backup
```

**Output** — every command supports `--json` for structured output
with a stable schema. Text output:

- locations: `file:line:col`
- locations with `--context=N`: `file:line:col` header, then N lines
  before/after; blank line between matches.
- diagnostics: `file:line:col: severity [source]: message`.
- symbol listings: `name\tkind\tfile:line:col`.

## Per-command

### `def`, `impl` — locate

```
usage: gopls def  SYMBOL --in FILE [flags]
       gopls impl SYMBOL --in FILE [flags]
       gopls <cmd> POS             [flags]

  --body        (def only) include the full declaration source
  --context=N   (impl only) N lines of surrounding source per match
  --json        JSON output
```

`def` returns one location; `--body` folds the decl source into that
response. `impl` returns many locations; per-match context via
`--context=N` is the right shape for reading them.

### `refs` — references

```
usage: gopls refs SYMBOL --in FILE [flags]
       gopls refs POS             [flags]

  --context=N   N lines of surrounding source per match
  --json        JSON output
```

Declaration is always included.

### `hover` — signature and doc

```
usage: gopls hover SYMBOL --in FILE [flags]
       gopls hover POS             [flags]

  --body   include the full declaration source in addition to the signature
  --json   JSON output
```

### `symbols` — top-level symbols in a file

```
usage: gopls symbols FILE [flags]

  --signatures   include signatures for each symbol (default on)
  --json         JSON output
```

### `wsymbols` — workspace symbol search

```
usage: gopls wsymbols QUERY [flags]

  --json   JSON output
```

### `callers`, `callees` — call graph

```
usage: gopls callers SYMBOL --in FILE [flags]
       gopls callees SYMBOL --in FILE [flags]
       gopls <cmd> POS             [flags]

  --depth=N     transitive depth (default 1)
  --context=N   N lines of surrounding source per call site
  --json        JSON output
```

### `check`, `vet` — diagnostics (with optional auto-fix)

```
usage: gopls check [FILE...] [flags]
       gopls vet   [FILE...] [flags]

no args: report for the whole workspace.
with args: report for each given file.

  --severity=S   filter: hint, info, warning, error (default: all)
  --fix          apply quickfix code actions to fixable diagnostics,
                 then report remaining diagnostics.
  [edit flags: -w, -d, -l, --preserve] (only with --fix)
  --json         JSON output
```

Runs the user's configured analyzer set (including `staticcheck` if
enabled). Matches the diagnostics shown in the editor. `gopls vet`
is a severity/source filter over `gopls check`, scoped to the
traditional `cmd/vet` analyzer set — different analyzer coverage
than the standalone `go vet` binary.

`--fix` applies diagnostic-associated quickfix code actions in a
single pass. Actions not tied to a diagnostic (refactors,
`source.organizeImports`, etc.) require `gopls codeaction` with an
explicit position.

### `rename` — validate and apply

```
usage: gopls rename SYMBOL --in FILE --to NEW [flags]
       gopls rename POS --to NEW              [flags]

validates the rename is legal, then applies it. on invalid position,
reports the problem without writing.

  --to NEW      new name (required)
  --dry-run     validate and print the edit without applying
  [edit flags: -w, -d, -l, --preserve]
  --json        JSON output
```

### `format`, `imports` — apply formatter / organize imports

```
usage: gopls format  FILE... [flags]
       gopls imports FILE... [flags]

  [edit flags: -w, -d, -l, --preserve]
  --json   JSON output
```

Applies the user's gopls formatter settings (e.g. `gofumpt`,
`formatting.local`). For workspace projects, prefer `gopls imports`
over `goimports` — it handles `go.work`, replace directives, and the
module graph correctly. `gofmt`/`goimports` remain fine for ad-hoc
use outside a workspace.

### `codeaction` — list or apply a code action

```
usage: gopls codeaction POS [flags]

with no --kind: list available actions at POS without applying.
with --kind:    apply one matching action.

  --kind KIND   code action kind (e.g. quickfix, refactor.extract,
                source.organizeImports)
  [edit flags: -w, -d, -l, --preserve]
  --json        JSON output
```

Covers all action kinds — refactors, source-level actions, and
quickfixes. For bulk quickfix application across a file set, use
`gopls check --fix` instead.

## Low-level primitives: `gopls cli <verb>`

A narrowed `gopls cli` namespace hosts LSP methods 1:1 for
debugging, conformance checks, and reproducible bug reports. Not
intended for agent skills — use the top-level commands instead.

```
gopls cli execute CMD [ARGS]    # workspace/executeCommand
gopls cli semtok FILE           # textDocument/semanticTokens/full
gopls cli links FILE            # textDocument/documentLink
gopls cli codelens FILE         # textDocument/codeLens
gopls cli folding-range FILE    # textDocument/foldingRange
gopls cli highlight POS         # textDocument/documentHighlight
gopls cli signature POS         # textDocument/signatureHelp
gopls cli prepare-rename POS    # textDocument/prepareRename
```

`--json` emits the server response verbatim; text output is a
best-effort pretty-print. Output shapes are not stable — see
[gopls-cli-migration.md](gopls-cli-migration.md#low-level-lsp-primitives-under-gopls-cli)
for the contract and rationale.

## Deliberately absent

Design choices not already captured in
[gopls-cli-migration.md](gopls-cli-migration.md)'s migration table:

- **No `gopls show` / `gopls explain` umbrella command.** `def` /
  `hover` / `refs` each answer a different question and return a
  different shape. Agents pick the one that matches the question.
- **No `gopls fix-all`.** Iteration across diagnostics is the agent's
  responsibility, not the CLI's.
- **No `gopls cli def` / `cli refs` / ... duplicate of top-level
  commands.** `gopls cli` holds only LSP methods without an
  answer-shaped top-level equivalent (plus three inspection-path
  exceptions for `highlight`, `signature`, `prepare-rename`).
  Rationale in the migration doc.
