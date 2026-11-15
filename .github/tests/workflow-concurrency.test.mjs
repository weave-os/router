import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import test from "node:test";

const workflowsDir = new URL("../workflows/", import.meta.url);

// Minimal evaluator for the GitHub expression subset used in concurrency keys:
// context lookups, string/boolean literals, ==, !=, &&, || and parentheses.
function evaluate(expression, context) {
  const tokens = [];
  const token = /\s*('(?:[^']|'')*'|==|!=|&&|\|\||[()]|[A-Za-z_][\w.-]*)\s*/y;
  while (token.lastIndex < expression.length) {
    const start = token.lastIndex;
    const match = token.exec(expression);
    assert.ok(match, `unsupported syntax at ${JSON.stringify(expression.slice(start))} in ${expression}`);
    tokens.push(match[1]);
  }
  let position = 0;
  const peek = () => tokens[position];
  const next = () => tokens[position++];
  const primary = () => {
    const token = next();
    if (token === "(") {
      const value = or();
      assert.equal(next(), ")", `unbalanced parentheses in ${expression}`);
      return value;
    }
    if (token.startsWith("'")) return token.slice(1, -1).replaceAll("''", "'");
    if (token === "true") return true;
    if (token === "false") return false;
    if (token === "null") return null;
    return token.split(".").reduce((value, key) => value?.[key] ?? null, context);
  };
  const comparison = () => {
    let left = primary();
    while (peek() === "==" || peek() === "!=") {
      const operator = next();
      const right = primary();
      left = operator === "==" ? left === right : left !== right;
    }
    return left;
  };
  const and = () => {
    let left = comparison();
    while (peek() === "&&") {
      next();
      const right = comparison();
      left = left ? right : left;
    }
    return left;
  };
  const or = () => {
    let left = and();
    while (peek() === "||") {
      next();
      const right = and();
      left = left || right;
    }
    return left;
  };
  const value = or();
  assert.equal(position, tokens.length, `unparsed tokens in ${expression}`);
  return value;
}

function render(raw, context) {
  const whole = raw.match(/^\$\{\{(.*)\}\}$/s);
  if (whole) return evaluate(whole[1], context);
  if (raw === "true" || raw === "false") return raw === "true";
  return raw.replace(/\$\{\{(.*?)\}\}/g, (_, expression) => String(evaluate(expression, context) ?? ""));
}

function triggers(lines) {
  const start = lines.findIndex((line) => /^on:/.test(line));
  if (start === -1) return new Set();
  const inline = lines[start].replace(/^on:\s*/, "").replace(/#.*/, "").trim();
  if (inline) return new Set(inline.replace(/[[\]]/g, "").split(",").map((name) => name.trim()));
  const names = new Set();
  for (const line of lines.slice(start + 1)) {
    if (/^\S/.test(line)) break;
    const match = line.match(/^ {2}([A-Za-z_]+):/);
    if (match) names.add(match[1]);
  }
  return names;
}

function concurrencyBlocks(lines) {
  const blocks = [];
  lines.forEach((line, index) => {
    const match = line.match(/^(\s*)concurrency:\s*$/);
    if (!match) return;
    const block = {};
    for (const child of lines.slice(index + 1)) {
      const indent = child.match(/^(\s*)/)[1].length;
      if (child.trim() && indent <= match[1].length) break;
      const field = child.match(/^\s+(group|cancel-in-progress):\s*(.+?)\s*$/);
      if (field) block[field[1]] = field[2];
    }
    blocks.push(block);
  });
  return blocks;
}

const workflows = readdirSync(workflowsDir)
  .filter((name) => /\.ya?ml$/.test(name))
  .map((name) => {
    const lines = readFileSync(new URL(name, workflowsDir), "utf8").split("\n");
    return { name, triggers: triggers(lines), concurrency: concurrencyBlocks(lines) };
  });

const pushToMain = (sha) => ({
  github: { event_name: "push", ref: "refs/heads/main", sha, event: {} },
});
const pullRequest = (number, sha) => ({
  github: { event_name: "pull_request", ref: `refs/pull/${number}/merge`, sha, event: { pull_request: { number } } },
});

test("the push-triggered Test workflow is covered", () => {
  const testWorkflow = workflows.find(({ name }) => name === "test.yml");
  assert.ok(testWorkflow.triggers.has("push"));
  assert.equal(testWorkflow.concurrency.length, 1);
});

test("a newer main commit never cancels or replaces the run of an earlier main commit", () => {
  for (const { name, triggers: events, concurrency } of workflows) {
    if (!events.has("push")) continue;
    for (const block of concurrency) {
      const earlier = pushToMain("a".repeat(40));
      const later = pushToMain("b".repeat(40));
      assert.notEqual(render(block.group, earlier), render(block.group, later), `${name}: main commits share a concurrency group`);
      assert.ok(!render(block["cancel-in-progress"] ?? "false", later), `${name}: main runs cancel in progress`);
    }
  }
});

test("a newer pull request commit still cancels the superseded run of the same pull request only", () => {
  for (const { name, triggers: events, concurrency } of workflows) {
    if (!events.has("pull_request")) continue;
    for (const block of concurrency) {
      const earlier = pullRequest(7, "a".repeat(40));
      const later = pullRequest(7, "b".repeat(40));
      const otherPullRequest = pullRequest(8, "c".repeat(40));
      assert.equal(render(block.group, earlier), render(block.group, later), `${name}: one pull request spans groups`);
      assert.notEqual(render(block.group, later), render(block.group, otherPullRequest), `${name}: pull requests share a group`);
      assert.equal(render(block["cancel-in-progress"] ?? "false", later), true, `${name}: superseded PR runs keep running`);
    }
  }
});

test("expressions outside the supported subset fail instead of evaluating differently", () => {
  assert.throws(() => evaluate("!(github.event_name != 'push')", pushToMain("a".repeat(40))), /unsupported syntax/);
  assert.equal(evaluate(" github.event_name == 'push' ", pushToMain("a".repeat(40))), true);
});
