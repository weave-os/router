# Release signing for fetched installer code

The installed statusline, slash-command wrappers and Codex status hook refresh
themselves from the network, and `install.sh --uninstall` can fetch its
uninstaller. Those paths are listed under "Fetched-code surfaces" in
`install.sh`. This file describes the signed release manifest they check
downloaded bytes against, the key it is signed with, and how a release is
signed.

## Files

| File | Contents |
| ---- | -------- |
| `install/MANIFEST` | One line per fetched file: `<sha256>  <path>`, the exact output of `sha256sum` (or `shasum -a 256`) run from the repository root, sorted by path. |
| `install/MANIFEST.sig.b64` | The signature over `install/MANIFEST`, base64 encoded on one line so the commit stays text-only. Empty until a release is signed. |
| `WEAVE_RELEASE_PUBKEY_PEM` | The verify-only public key, embedded near the top of `cc-statusline.sh`, `codex-status.sh` and `install.sh` (including the copies `install.sh` embeds). |

Paths in the manifest are relative to the repository root, for example
`install/cc-statusline.sh` and `install/commands/router-status.md`. The
manifest covers `install/cc-statusline.sh`, `install/codex-status.sh`,
`install/uninstall.sh` and every file under `install/commands/`.

A client verifies in this order and refuses the swap if any step fails:

1. Decode `MANIFEST.sig.b64` and check it over the fetched `MANIFEST` with
   `openssl dgst -sha256 -verify <pubkey> -signature <sig> MANIFEST`.
2. Find the row whose path matches the file it is about to replace, and compare
   that row's digest with the SHA-256 of the downloaded body.
3. Only then apply the existing shape checks and move the body into place.

## Key

The key is RSA-2048 and the signature is RSA PKCS#1 v1.5 over SHA-256. Ed25519
would be smaller, but `openssl dgst -sha256` cannot sign or verify with it, and
the LibreSSL that macOS ships as `openssl` has no Ed25519 support in
`pkeyutl`. RSA verifies with the same command on every platform the installer
supports.

The key committed today is a **placeholder**. It is a real public key so the
verification code parses it, but its private half was discarded when it was
generated. Until the release pipeline replaces it, no manifest verifies, so
every fetched-code path fails closed.

## Signing a release

The private key never enters this repository. It is held by the release
pipeline, and its custody and access controls are owned there.

Generate a key pair once:

```bash
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out release-signing.key
openssl pkey -in release-signing.key -pubout -out release-signing.pub
```

Replace the PEM body between the `WEAVE_RELEASE_PUBKEY_EOF` markers with
`release-signing.pub` in all five places (`cc-statusline.sh`,
`codex-status.sh`, and `install.sh` at the top plus its two embedded copies).
Then, for each release, from the repository root:

```bash
{ sha256sum install/cc-statusline.sh install/codex-status.sh install/uninstall.sh \
    install/commands/*.md; } | sort -k2 > install/MANIFEST
openssl dgst -sha256 -sign release-signing.key install/MANIFEST \
  | openssl base64 -A > install/MANIFEST.sig.b64
echo >> install/MANIFEST.sig.b64
```

Regenerate `MANIFEST` after the key is stamped, because the embedded key is
part of the hashed scripts. Check the result before tagging:

```bash
openssl base64 -d -A < install/MANIFEST.sig.b64 > /tmp/MANIFEST.sig
openssl dgst -sha256 -verify release-signing.pub -signature /tmp/MANIFEST.sig install/MANIFEST
sha256sum -c install/MANIFEST
```

A `MANIFEST` on a branch between releases may lag the scripts it lists. Only
the copy at a signed release tag is authoritative.

## Release source and the main-branch alias

`cc-statusline.sh` and `codex-status.sh` download replacements only from an
immutable release tag. Each copy carries `WEAVE_RELEASE_TAG`, the tag of the
release it belongs to, stamped by the release pipeline. A refresh:

1. Downloads the copy of the same script on `main` and reads its
   `WEAVE_RELEASE_TAG="router-vX.Y.Z"` line. That copy is never executed; only
   the tag name is used, and it must be a well-formed `router-vX.Y.Z` tag.
2. Stops if that tag is not strictly newer than its own, so a refresh never
   moves to an older release.
3. Downloads `install/MANIFEST`, `install/MANIFEST.sig.b64` and the script
   from `raw.githubusercontent.com/weave-os/router/<tag>/`, verifies them as
   described above, and swaps the script in only if every check passes.
4. Refuses a verified body whose own `WEAVE_RELEASE_TAG` is not `<tag>`. The
   signature covers file digests, not a release name, so without this check
   an older signed release republished under a newer tag name would pass. A
   body fetched from an overridden URL must likewise carry a tag newer than
   the installed copy's.

Anyone who can write to `main` can change the advertised tag, but only to
another tag that is newer than the installed one and that carries a
`MANIFEST` signed with the release key. The signature, not the tag, is the
trust boundary. When `WEAVE_STATUSLINE_URL` or `WEAVE_CODEX_STATUS_URL`
overrides the source, the advertised tag is not read: the script is fetched
from that URL, `MANIFEST` and `MANIFEST.sig.b64` from the same directory, and
the same verification applies.

The wrapper refresh in `cc-statusline.sh` reads `MANIFEST`, `MANIFEST.sig.b64`
and the `commands/*.md` bodies from the installed copy's own tag, or from the
parent of `WEAVE_COMMANDS_URL_BASE` when that overrides the source. After the
signature verifies it also downloads the `cc-statusline.sh` next to that
`MANIFEST`, checks it against its row and requires its `WEAVE_RELEASE_TAG` to
be no older than the installed copy's or the release recorded in
`.manifest-tag` in the baseline cache, which each verified cycle rewrites with
the release it accepted and the command directory it applies to. Without this, an older signed release served from a
mutable source would verify and roll the wrappers back. Every release
must therefore publish `install/cc-statusline.sh` and list it in `MANIFEST`.

Release operations must keep these invariants:

- **The main-branch copies stay manifest-aware, indefinitely.** Copies
  installed before signed releases still fetch
  `main/install/cc-statusline.sh` and `main/install/codex-status.sh` directly
  and apply only shape checks. The copies on `main` are how they move onto the
  verified, tag-pinned flow at their normal refresh interval, so those paths
  must never be removed, redirected or replaced with a script that skips
  verification.
- **`WEAVE_RELEASE_TAG` on `main` names the newest signed release.** Stamp it
  in both scripts when a release is signed, and only after the tag's
  `MANIFEST.sig.b64` verifies. A tag without a valid signature makes every
  client skip the update.
- **Release tags are never moved or reused.** Clients treat the contents at a
  tag as fixed.

## Wrapper baselines and the re-seed sentinel

The wrapper refresh replaces an installed `.claude/commands/*.md` file only
when its bytes still equal a cached baseline, so a wrapper the user edited is
left alone. Baselines written before manifest verification existed (by
`install.sh`, or by an older statusline that installed unverified downloads)
cannot be trusted as the "what we shipped" reference.

The sentinel `.manifest-seeded` in the baseline cache directory
(`${XDG_CACHE_HOME:-~/.cache}/weave-router/`) records that the baselines were
seeded under the manifest:

- **Absent:** the first refresh whose `MANIFEST` verifies re-seeds once. Each
  verified body becomes the baseline, an installed wrapper that Router owns and
  that differs from it is replaced with it, and the sentinel is written. A
  wrapper counts as Router-owned when it carries the in-body ownership marker,
  has a `<name>.md.weave-router` ownership sidecar that still records its exact
  body, or still equals the old baseline; a command with none of these is the
  user's and is never replaced.
  A wrapper whose body could not be fetched or verified in that cycle has its
  old baseline removed.
- **Present:** the baseline comparison gates the swap as before, so local edits
  are preserved.

A wrapper that carries the in-body marker and was edited before upgrading is
reset once, on the first post-upgrade refresh. A cycle that runs under
`WEAVE_STATUSLINE_TRUST_UNSIGNED=1` never re-seeds or writes the sentinel, so
returning to a signed source still re-seeds once.

## Turning verification or updates off

- `WEAVE_STATUSLINE_UPDATE=0` turns off every background network path in the
  statusline, including both refreshes. It is unchanged by signed releases and
  remains the way to stop fetching code entirely, including on machines without
  `openssl`, where every refresh is refused anyway.
- `WEAVE_STATUSLINE_TRUST_UNSIGNED=1` skips signature and digest verification,
  but only for a source overridden (`WEAVE_STATUSLINE_URL`,
  `WEAVE_COMMANDS_URL_BASE`, `WEAVE_CODEX_STATUS_URL`, `WEAVE_UNINSTALL_URL`)
  to a host other than `raw.githubusercontent.com`. Each use prints a warning
  on stderr. It provides no security property: it exists only so self-hoster
  forks do not need a signing pipeline of their own, and a tampered replacement
  script can remove the gate. The default publisher always verifies.

## Key rotation

Clients trust only the key embedded in the copy they are running. To rotate,
ship a release signed with the old key whose scripts embed the new key; once
installed copies have refreshed onto it, later releases are signed with the new
key. The operational procedure (cadence, who can sign, how a compromised key is
revoked) belongs with release signing and is out of scope here.

## Scope

The installer code in this repository adds only the verification path and the
embedded public-key slot. Generating the real key pair, holding the private
key, stamping the public key and `WEAVE_RELEASE_TAG`, signing each release and
the key-rotation procedure are owned by the release pipeline.
