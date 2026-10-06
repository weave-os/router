import assert from "node:assert/strict";
import test from "node:test";
import { notifyDownstream, NotificationOutcome } from "../scripts/notify-downstream.mjs";

const repository = "example/router";
const sourceSHA = "a".repeat(40);
const configuration = { repository: "example/downstream", workflow: "sync.yml", ref: "main" };
const successfulRun = {
  id: 123, conclusion: "success", event: "push", head_branch: "main", head_sha: sourceSHA,
  head_repository: { full_name: repository }, path: ".github/workflows/test.yml",
};

function scenario(run = successfulRun) {
  const calls = [];
  const request = async (url, options) => {
    calls.push({ url, options });
    if (url.endsWith("/actions/runs/123")) return new Response(JSON.stringify(run));
    if (url.endsWith("/commits/main")) return new Response(JSON.stringify({ sha: sourceSHA }));
    return new Response(null, { status: 204 });
  };
  const input = {
    event: { workflow_run: run }, repository, sourceToken: "source-token", dispatchToken: "dispatch-token",
    destination: JSON.stringify(configuration),
  };
  return { calls, request, input };
}

test("successful current main tests dispatch the configured workflow with separate credentials", async () => {
  const { calls, request, input } = scenario();
  assert.equal(await notifyDownstream(input, request), NotificationOutcome.DISPATCHED);
  assert.equal(calls.length, 3);
  for (const call of calls.slice(0, 2)) {
    assert.equal(call.options.headers.Authorization, "Bearer source-token");
    assert.equal(call.options.method, "GET");
  }
  assert.equal(calls[2].url, "https://api.github.com/repos/example/downstream/actions/workflows/sync.yml/dispatches");
  assert.equal(calls[2].options.headers.Authorization, "Bearer dispatch-token");
  assert.equal(calls[2].options.method, "POST");
  assert.deepEqual(JSON.parse(calls[2].options.body), { ref: "main" });
  assert.equal(calls[2].options.redirect, "error");
});

test("PR, fork, failed, cancelled and non-main completions never access destination credentials", async () => {
  for (const override of [
    { event: "pull_request" }, { head_repository: { full_name: "fork/router" } },
    { conclusion: "failure" }, { conclusion: "cancelled" }, { head_branch: "feature" },
  ]) {
    const { calls, request, input } = scenario({ ...successfulRun, ...override });
    input.destination = "not JSON";
    assert.equal(await notifyDownstream(input, request), NotificationOutcome.INELIGIBLE);
    assert.equal(calls.length, 0);
  }
});

test("canonical run provenance and current main are checked before dispatch", async () => {
  for (const canonicalRun of [
    { ...successfulRun, path: ".github/workflows/other.yml" },
    { ...successfulRun, event: "pull_request" },
    { ...successfulRun, head_sha: "b".repeat(40) },
  ]) {
    const { calls, input } = scenario();
    assert.equal(await notifyDownstream(input, async (url, options) => {
      calls.push({ url, options });
      return new Response(JSON.stringify(url.endsWith("/commits/main") ? { sha: sourceSHA } : canonicalRun));
    }), NotificationOutcome.INELIGIBLE);
    assert.ok(calls.every(call => call.options.method !== "POST"));
  }
});

test("unconfigured destinations skip and invalid destinations cannot send requests", async () => {
  const { calls, request, input } = scenario();
  assert.equal(await notifyDownstream({ ...input, destination: "" }, request), NotificationOutcome.DISABLED);
  for (const destination of ["not JSON", JSON.stringify({ ...configuration, repository: "https://evil.invalid" }),
    JSON.stringify({ ...configuration, workflow: "../sync.yml" }), JSON.stringify({ ...configuration, ref: "" })]) {
    await assert.rejects(notifyDownstream({ ...input, destination }, request), error => {
      assert.doesNotMatch(error.message, /evil|sync\.yml|example/);
      return true;
    });
  }
  assert.equal(calls.length, 0);
});

test("API and network failures do not disclose destination or token in errors", async () => {
  const { input } = scenario();
  for (const request of [
    async () => new Response("private details", { status: 403 }),
    async () => { throw new Error("private endpoint and token"); },
  ]) {
    await assert.rejects(notifyDownstream(input, request), error => {
      assert.match(error.message, /GitHub request failed/);
      assert.doesNotMatch(error.message, /private|example|token/);
      return true;
    });
  }
});


test("configured ref whitespace is removed before dispatch", async () => {
  const { calls, request, input } = scenario();
  input.destination = JSON.stringify({ ...configuration, ref: " main " });
  assert.equal(await notifyDownstream(input, request), NotificationOutcome.DISPATCHED);
  assert.deepEqual(JSON.parse(calls[2].options.body), { ref: "main" });
});

test("malformed source JSON never reaches the reported error", async () => {
  const { input } = scenario();
  for (const malformedCall of [1, 2]) {
    let calls = 0;
    await assert.rejects(notifyDownstream(input, async () => {
      calls += 1;
      return new Response(calls === malformedCall ? "BODYMARKER secret response" : JSON.stringify(successfulRun));
    }), error => {
      assert.equal(error.message, "GitHub response was not valid JSON (HTTP 200)");
      assert.doesNotMatch(error.message, /BODYMARKER|secret/);
      return true;
    });
    assert.equal(calls, malformedCall);
  }
});
