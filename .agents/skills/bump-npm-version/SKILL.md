---
name: bump-npm-version
description: Bump and publish a new version of the installer npm package (@weave-os/router, plus the legacy @workweave/router mirror). Use when asked to "bump the npm version", "publish a new npm version", or "deploy a new installer" after changes under install/ have merged to main.
---

# Bump the npm version

The installer ships as `@weave-os/router` (and the legacy `@workweave/router` mirror) from `install/npm/`. Publishing is triggered by pushing a `router-vX.Y.Z` tag; [`.github/workflows/publish_npm.yml`](../../../.github/workflows/publish_npm.yml) then runs the installer tests, checks the tag equals `install/npm/package.json`'s version, and publishes both names via npm OIDC. There is no npm token to handle.

Order matters: **bump merged to main first, tag second.** The workflow rejects tags not reachable from `origin/main` or whose version differs from `package.json`.

## Steps

1. **Find the current version and confirm the changes are on main.**
   ```bash
   git fetch origin main --tags -q
   git show origin/main:install/npm/package.json | grep '"version"'
   git tag -l 'router-v*' | sort -V | tail -3
   npm view @weave-os/router version
   ```
   The tag, package.json and npm should agree; if they don't, stop and investigate. Check `git log <last-tag>..origin/main -- install/` to confirm there is something to ship. Default to a patch bump unless the user says otherwise.

2. **Open a bump PR** from a branch off `origin/main` that changes only the `version` in `install/npm/package.json`:
   ```bash
   git checkout -b <user>/bump-npm-X.Y.Z origin/main
   sed -i '' 's/"version": "OLD"/"version": "NEW"/' install/npm/package.json   # GNU sed: drop the ''
   git commit -am "chore(install): bump npm package to NEW"
   git push -u origin HEAD
   gh pr create --base main --title "chore(install): bump npm package to NEW" --body "<what installer changes this ships>"
   ```
   Follow the repo's commit/PR attribution rules, and never put customer or org names in the title or body (public repo).

3. **Wait for checks and merge** (`gh pr checks <n> --watch`, then `gh pr merge <n> --squash`, or `--auto` if checks are still pending). Branch protection applies, so don't bypass it.

4. **Tag the merge commit and push the tag.** Use the squash commit on main, not your branch head:
   ```bash
   git fetch origin main -q
   git tag -a -m "router-vNEW" router-vNEW origin/main   # confirm `git show origin/main:install/npm/package.json` is NEW first
   git push origin router-vNEW   # annotated (-a -m) so it works with tag.gpgSign/message-required configs
   ```
   Pushing the tag publishes to a public registry and can't be undone (npm versions can't be republished). Confirm with the user first unless they already asked for the publish.

5. **Watch the workflow and verify.**
   ```bash
   gh run list --workflow publish_npm.yml --limit 1
   gh run watch <run-id> --exit-status
   npm view @weave-os/router version && npm view @workweave/router version
   ```
   Both should print the new version. npm's registry can lag a minute.

## Failure modes

- **"not reachable from origin/main"**: the tag points at a non-main commit. Delete it (`git push origin :router-vNEW`, `git tag -d router-vNEW`) and re-tag `origin/main`.
- **"Tag version does not match package.json"**: the bump PR hadn't merged, or the tag is on the wrong commit. Delete the tag and re-tag.
- **Installer tests fail**: fix on main with a new PR, then re-tag. If nothing was published, the same version can be reused after deleting the tag.
- **Primary package published but legacy `@workweave/router` step failed**: that version is burned on `@weave-os/router`. Bump again rather than retrying.
- **401 at publish**: the trusted-publisher config on npmjs.com (org `workweave`, repo `router`, workflow `publish_npm.yml`) is broken for one of the two packages.
