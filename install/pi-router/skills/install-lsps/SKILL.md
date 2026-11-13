---
name: install-lsps
description: Install language servers (gopls, typescript-language-server, pyright, rust-analyzer) and their prerequisite toolchains (Go, Node/npm, rustup) so the lsp tool works. Use when the user asks to enable or install LSP / language-server support, when lsp_enable reports a missing toolchain, or when the lsp tool reports no language server is installed.
---

# Install LSPs

Enable the `lsp` tool for a language by installing its language server — and,
when the server install needs it, the underlying toolchain.

## Rules

- Everything here is consent-gated: only act on the user's explicit go-ahead,
  and confirm each toolchain install separately — those are system-level
  changes, not part of whatever task prompted this.
- If the user declines, stop. If they ask not to be offered again, call
  `lsp_enable` with `{"language": "<language>", "action": "dismiss"}`.
- Prefer the `lsp_enable` tool over bash whenever the toolchain already
  exists; reach for bash only for the prerequisites `lsp_enable` refuses to
  install itself.

## Per language

Work the ladder for each language the user wants:

1. **Check what is missing.** Server binaries: `gopls`,
   `typescript-language-server`, `pyright-langserver` (or
   `basedpyright-langserver`), `rust-analyzer` — also look in `~/go/bin` and
   `~/.cargo/bin`, which the lsp tool searches even when they are off PATH.
   Toolchains: `go`, `npm`, `rustup`.
2. **Toolchain present** → call `lsp_enable` with
   `{"language": "<go|typescript|python|rust>"}` and you are done; it runs the
   right server install and verifies the binary resolves.
3. **Toolchain missing** → show the user the command you intend to run, get
   their confirmation, run it in bash, then return to step 2.
4. **Verify** with a real `lsp` query (`documentSymbol` on an actual source
   file). The first query per workspace can take up to a minute while the
   server indexes; that is normal.

## Toolchain installs (step 3 reference)

Go (for gopls):

```bash
brew install go            # macOS
sudo apt-get install -y golang-go   # Debian/Ubuntu; or dnf install golang
```

Or the official downloads at https://go.dev/dl for anything else.

Node/npm (for typescript-language-server and pyright):

```bash
brew install node          # macOS
sudo apt-get install -y nodejs npm  # Debian/Ubuntu
```

Or nvm (https://github.com/nvm-sh/nvm) when the user prefers per-user installs.

rustup (for rust-analyzer):

```bash
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
```

This is the official rust installer; show the user the command before running
it and follow its printed PATH instructions afterwards.

## Notes

- `go install` drops gopls in `~/go/bin` and rustup drops rust-analyzer in
  `~/.cargo/bin`. The lsp tool finds both without PATH changes; other tools
  may still want a PATH entry.
- All of this can also happen at install time:
  `npx @weave-os/router --pi --lsp go,typescript,python,rust`.
