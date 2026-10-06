import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

export const NotificationOutcome = Object.freeze({
  DISPATCHED: "dispatched", INELIGIBLE: "ineligible", DISABLED: "disabled",
});
const RunConclusion = Object.freeze({ SUCCESS: "success" });
const RunEvent = Object.freeze({ PUSH: "push" });
const MainBranch = "main";
const TestWorkflowPath = ".github/workflows/test.yml";

const eligibleRun = (run, repository) => run?.conclusion === RunConclusion.SUCCESS &&
  run.event === RunEvent.PUSH && run.head_branch === MainBranch && run.head_repository?.full_name === repository;

async function githubRequest(request, path, token, body) {
  let response;
  try {
    response = await request(`https://api.github.com/${path}`, {
      method: body ? "POST" : "GET",
      headers: { Authorization: `Bearer ${token}`, Accept: "application/vnd.github+json", "Content-Type": "application/json" },
      body: body ? JSON.stringify(body) : undefined,
      redirect: "error",
      signal: AbortSignal.timeout(30_000),
    });
  } catch {
    throw new Error("GitHub request failed (network or timeout)");
  }
  // A downstream repository may be private; never print endpoints or responses.
  assert.ok(response.ok, `GitHub request failed (HTTP ${response.status})`);
  if (body) return;
  try { return await response.json(); }
  catch { throw new Error(`GitHub response was not valid JSON (HTTP ${response.status})`); }
}

export async function notifyDownstream({ event, repository, sourceToken, dispatchToken, destination }, request = fetch) {
  if (!eligibleRun(event.workflow_run, repository)) return NotificationOutcome.INELIGIBLE;
  if (!destination) return NotificationOutcome.DISABLED;
  let configuration;
  try { configuration = JSON.parse(destination); }
  catch { throw new Error("Invalid downstream workflow configuration"); }
  assert.ok(typeof configuration?.repository === "string" && /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(configuration.repository), "Invalid downstream repository configuration");
  assert.ok(typeof configuration?.workflow === "string" && /^[A-Za-z0-9_.-]+\.ya?ml$/.test(configuration.workflow), "Invalid downstream workflow configuration");
  assert.ok(typeof configuration.ref === "string" && configuration.ref.trim(), "Downstream workflow ref is required");
  assert.ok(sourceToken && dispatchToken, "Downstream workflow credentials are required");
  assert.ok(Number.isSafeInteger(event.workflow_run.id) && event.workflow_run.id > 0, "Invalid source run identity");
  const run = await githubRequest(request, `repos/${repository}/actions/runs/${event.workflow_run.id}`, sourceToken);
  if (!eligibleRun(run, repository) || run.path !== TestWorkflowPath) return NotificationOutcome.INELIGIBLE;
  // Main can advance between this read and dispatch; downstream must revalidate.
  if ((await githubRequest(request, `repos/${repository}/commits/${MainBranch}`, sourceToken)).sha !== run.head_sha) return NotificationOutcome.INELIGIBLE;
  await githubRequest(request,
    `repos/${configuration.repository}/actions/workflows/${configuration.workflow}/dispatches`,
    dispatchToken, { ref: configuration.ref.trim() });
  return NotificationOutcome.DISPATCHED;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  notifyDownstream({
    event: JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, "utf8")),
    repository: process.env.GITHUB_REPOSITORY,
    sourceToken: process.env.GITHUB_TOKEN,
    dispatchToken: process.env.DOWNSTREAM_WORKFLOW_TOKEN,
    destination: process.env.DOWNSTREAM_WORKFLOW_DISPATCH,
  }).then(outcome => console.log(`Downstream workflow notification: ${outcome}`))
    .catch(error => { console.error(error.message); process.exitCode = 1; });
}
