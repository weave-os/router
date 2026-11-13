---
name: lsp-guide
description: Recipes for the lsp tool — resolving where a symbol is defined, every place it is used, type signatures and docs, file outlines, and compiler/type errors through a real language server (Go, TypeScript/JavaScript, Python, Rust). Use when navigating or explaining code by symbol, finding callers or usages, checking what broke after an edit, or deciding between lsp and text search.
---

# Using the lsp tool

The `lsp` tool answers *semantic* questions through the language's own server:
it resolves through imports, types, and aliases, where grep only matches text.
Reach for it whenever the question is about a **symbol**; keep grep for plain
strings, comments, and file types no server supports.

## Operations

| Operation | Answers | Needs |
|---|---|---|
| `definition` | where the symbol at a position is defined | `path`, `line`, `column` |
| `references` | every place the symbol at a position is used | `path`, `line`, `column` |
| `hover` | type signature + docs for the symbol at a position | `path`, `line`, `column` |
| `documentSymbol` | the outline (types, functions, methods) of one file | `path` |
| `diagnostics` | compiler/type errors and warnings for one file | `path` |

Positions are 1-based, and `column` must point **at the identifier** (its first
character works). All paths resolve against the working directory.

## Recipes

**"Where is X defined?"** — from any usage site of X:
`{"operation": "definition", "path": "<file>", "line": <L>, "column": <C>}`.
Don't know a usage site? Find one first (see next recipe's step 1).

**"Everywhere X is used"** — two steps, always:

1. Locate the declaration: `rg -n --column '\bX\b'` (the `--column` output is
   exactly the coordinate the next call needs), or `documentSymbol` when you
   already know the file.
2. `{"operation": "references", "path": "<decl file>", "line": <L>, "column": <C>}`.

A grep hit list is **not** a references answer: it includes comments and
strings, misses aliased imports, and can't distinguish same-named symbols.
Step 2 is what makes the answer authoritative.

**"What is this / what's its signature?"** — `hover` at the symbol. Prefer it
over reading the definition file when you only need the type or doc comment.

**"What's in this file?"** — `documentSymbol` before reading a large file; jump
straight to the line the outline gives you.

**"Did my edit break anything here?"** — `diagnostics` on the file after
editing. It reports the server's current analysis of the saved file.

## Behavior worth knowing

- **First query per workspace is slow** (the server indexes — up to a minute
  in a big repo). Later queries on that workspace are fast; don't give up
  after one timeout, retry once.
- **References are capped** (default 100, header says how many exist).
- **Unsupported file type or missing server** returns advice text, not an
  error — fall back to grep for that file. A missing server can be installed
  with the user's consent via `lsp_enable` (see the install-lsps skill for
  toolchain prerequisites).
- Works inside dispatch subagents too (they share the parent's warm servers),
  so symbol lookups are safe to fan out.
