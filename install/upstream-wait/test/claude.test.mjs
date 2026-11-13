import assert from 'node:assert/strict'
import { test } from 'node:test'
import { register } from '../claude-plugin/hooks/register.mjs'

async function modFixture() {
  const hooks = new Map()
  register((event, matcher, hook) => hooks.set(event, hook || matcher))
  const environment = { WEAVE_PROVIDER_WAIT_ENDPOINT: 'http://127.0.0.1:1234/capability', ANTHROPIC_BASE_URL: 'https://router.example.invalid' }
  const draws = []
  const requests = []
  let poll
  let waiting = false
  const $ = {
    env: { get: async (key) => environment[key], set: async (key, value) => { environment[key] = value } },
    http: { fetch: async (url, init) => {
      requests.push({ url, init })
      return { ok: true, text: JSON.stringify(url.endsWith('/configure') ? { baseURL: 'http://127.0.0.1:1234/capability/provider/fixture' } : { waiting }) }
    } },
    clock: { every: (interval, callback) => { poll = callback } },
    ui: { invalidate: async (event) => draws.push(event) },
    session: { id: async () => 'session-1' },
  }
  await hooks.get('session.start')($, {}, async (e) => e)
  const spinner = { component: 'Spinner', requestId: 'session-1', props: { message: null, word: 'Thinking', suffix: '…', mode: 'requesting' } }
  return { $, hooks, environment, requests, draws, spinner, poll: () => poll(), setWaiting: (value) => { waiting = value }, render: (event = spinner) => hooks.get('ui.render')($, event, async (e) => e) }
}

test('native spinner changes only after the real provider signal and leaves responses untouched', async () => {
  const fixture = await modFixture()
  const { $, hooks, spinner } = fixture
  const chunks = [{ kind: 'engine', ref: 1 }, { kind: 'text', index: 0, text: 'authored provider response' }]
  const answer = { answer: 'authored provider response', usage: null }
  const step = hooks.get('turn.step')($, { turnId: 'turn-1', index: 0 }, async function* () {
    yield* chunks
    return answer
  })
  assert.deepEqual((await step.next()).value, chunks[0])
  await fixture.poll()
  assert.deepEqual(await fixture.render(), spinner)
  fixture.setWaiting(true)
  await fixture.poll()
  const visible = await fixture.render()
  assert.equal(visible.props.message, 'Waiting on upstream provider..')
  assert.equal(visible.props.suffix, '')
  assert.equal(visible.props.word, spinner.props.word)
  assert.deepEqual(await fixture.render({ ...spinner, requestId: 'background-agent' }), { ...spinner, requestId: 'background-agent' })
  fixture.setWaiting(false)
  await fixture.poll()
  assert.deepEqual(await fixture.render(), spinner)
  assert.deepEqual((await step.next()).value, chunks[1])
  assert.deepEqual(await step.next(), { done: true, value: answer })
  assert.deepEqual(await fixture.render(), spinner)
  assert.equal(fixture.environment.ANTHROPIC_BASE_URL, 'http://127.0.0.1:1234/capability/provider/fixture')
  assert.equal(JSON.parse(fixture.requests[0].init.body).baseURL, 'https://router.example.invalid')
})

test('interruption, errors and session changes restore the native message without extra text', async () => {
  for (const completion of ['interrupt', 'error', 'session.end']) {
    const fixture = await modFixture()
    const { $, hooks, spinner } = fixture
    const step = hooks.get('turn.step')($, { turnId: 'turn-1' }, async function* () { yield { kind: 'engine', ref: 1 } })
    await step.next()
    fixture.setWaiting(true)
    await fixture.poll()
    assert.equal((await fixture.render()).props.message, 'Waiting on upstream provider..')
    if (completion === 'error') await assert.rejects(step.throw(new Error('authored failure')), /authored failure/)
    else if (completion === 'interrupt') await step.return()
    else {
      const returned = await hooks.get('session.end')($, { reason: 'clear' }, async () => ({}))
      assert.deepEqual(returned, {})
      await step.return()
    }
    assert.deepEqual(await fixture.render(), spinner)
  }
})

test('a pending poll cannot restore a spinner after the turn ends', async () => {
  const fixture = await modFixture()
  const { $, hooks, spinner } = fixture
  const step = hooks.get('turn.step')($, { turnId: 'turn-1' }, async function* () { yield { kind: 'engine', ref: 1 } })
  await step.next()
  let resolve
  $.http.fetch = () => new Promise((done) => { resolve = done })
  const pending = fixture.poll()
  assert.deepEqual(await hooks.get('turn.complete')($, { turnId: 'turn-1', isAborted: true }, async () => ({})), {})
  resolve({ ok: true, text: '{"waiting":true}' })
  await pending
  assert.deepEqual(await fixture.render(), spinner)
  await step.return()
})
