// Install commands for pointing a coding harness at *this* router.
//
// Self-hosted deployments serve this dashboard from the router's own origin, so
// the base URL is derived from the browser rather than hardcoded: an install
// command that silently pointed at the hosted endpoint would route a
// self-hoster's traffic off-box. `--base-url` is always passed explicitly for
// the same reason — install.sh otherwise falls back to the hosted default.

/** A harness the installer can configure, in the order the picker shows them. */
export type HarnessID = "claude" | "codex" | "pi";

/** Whether the installer writes machine-wide config or repo-local config. */
export type InstallScope = "user" | "project";

export interface HarnessInfo {
  id: HarnessID;
  label: string;
  /** What the installer touches, shown under the label in the picker. */
  detail: string;
  /** How the repo-local install is launched, shown on the project scope option. */
  projectDetail: string;
}

export const HARNESSES: HarnessInfo[] = [
  {
    id: "claude",
    label: "Claude Code",
    detail: "Patches ~/.claude/settings.json.",
    projectDetail: "Writes <repo>/.claude/settings.json — commit it to share with teammates.",
  },
  {
    id: "codex",
    label: "Codex",
    detail: "Patches ~/.codex/config.toml.",
    projectDetail: "Writes <repo>/.codex/config.toml (gitignored). Run: CODEX_HOME=.codex codex",
  },
  {
    id: "pi",
    label: "pi",
    detail: "Merges a weave provider into ~/.pi/agent/models.json.",
    projectDetail: "Writes <repo>/.pi/ (gitignored). Run: PI_CODING_AGENT_DIR=.pi pi",
  },
];

export function harness(id: HarnessID): HarnessInfo {
  const found = HARNESSES.find(h => h.id === id);
  if (found == null) throw new Error(`unknown harness: ${id}`);
  return found;
}

/**
 * Origin of the router serving this dashboard, with the /ui basePath stripped.
 * Returns "" during SSR (static export prerender), which callers render as a
 * disabled/placeholder state rather than a wrong command.
 */
export function routerOrigin(): string {
  if (typeof window === "undefined") return "";
  return window.location.origin;
}

/**
 * The npx one-liner that configures `harnessID` against this router. The token
 * rides in as a WEAVE_ROUTER_KEY env prefix, which install.sh reads to skip its
 * interactive prompt.
 *
 * `--package … -- <bin>` rather than `npx -y @weave-os/router`: npm <= 6's
 * bundled npx treats an undeclared flag as consuming the next token, so the
 * short form drops the package name and resolves `weave-router` from the
 * registry instead — with the key already in its environment.
 */
export function installCommand(
  harnessID: HarnessID,
  scope: InstallScope,
  token: string,
  origin: string,
): string {
  const flags = [`--${harnessID}`, `--scope ${scope}`, `--base-url ${shellSingleQuote(origin)}`];
  return `WEAVE_ROUTER_KEY=${shellSingleQuote(token)} npx --package @weave-os/router -y -- weave-router ${flags.join(" ")}`;
}

/**
 * Wraps a value in single quotes for POSIX shell, closing/escaping/reopening
 * any embedded quote via the standard '\'' idiom.
 */
function shellSingleQuote(value: string): string {
  return `'${value.replaceAll("'", `'\\''`)}'`;
}
