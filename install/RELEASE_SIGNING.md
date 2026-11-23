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

## Key rotation

Clients trust only the key embedded in the copy they are running. To rotate,
ship a release signed with the old key whose scripts embed the new key; once
installed copies have refreshed onto it, later releases are signed with the new
key. The operational procedure (cadence, who can sign, how a compromised key is
revoked) belongs with release signing and is out of scope here.
