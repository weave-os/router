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

/** Terminal the install command is pasted into; selects the env-var syntax in installCommand. */
export type InstallShell = "posix" | "powershell";

export const INSTALL_SHELLS: { value: InstallShell; label: string }[] = [
  { value: "posix", label: "macOS / Linux" },
  { value: "powershell", label: "Windows PowerShell" },
];

/** PowerShell for Windows browsers, the POSIX shell everywhere else and during SSR. */
export function defaultInstallShell(): InstallShell {
  if (typeof navigator === "undefined") return "posix";
  return /windows/i.test(navigator.userAgent) ? "powershell" : "posix";
}

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
 * rides in as WEAVE_ROUTER_KEY, which the installer reads to skip its
 * interactive prompt: a per-command env prefix in POSIX shells, a session
 * variable in PowerShell (which has no prefix form), removed after the run.
 * PowerShell calls npx.cmd because npx.ps1 is blocked by the Restricted
 * execution policy.
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
  shell: InstallShell,
): string {
  const quote = shell === "powershell" ? powerShellSingleQuote : shellSingleQuote;
  const flags = [`--${harnessID}`, `--scope ${scope}`, `--base-url ${quote(origin)}`];
  const npxArgs = `--package @weave-os/router -y -- weave-router ${flags.join(" ")}`;
  return shell === "powershell"
    ? `$env:WEAVE_ROUTER_KEY=${quote(token)}; npx.cmd ${npxArgs}; Remove-Item Env:WEAVE_ROUTER_KEY`
    : `WEAVE_ROUTER_KEY=${quote(token)} npx ${npxArgs}`;
}

/**
 * Wraps a value in single quotes for POSIX shell, closing/escaping/reopening
 * any embedded quote via the standard '\'' idiom.
 */
function shellSingleQuote(value: string): string {
  return `'${value.replaceAll("'", `'\\''`)}'`;
}

/** Wraps a value in PowerShell single quotes, where an embedded quote is doubled. */
function powerShellSingleQuote(value: string) {
  return `'${value.replaceAll("'", "''")}'`;
}
