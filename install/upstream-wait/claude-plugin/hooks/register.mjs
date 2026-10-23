const WAITING_MESSAGE = 'Waiting on upstream provider..'

export function register(on) {
  let endpoint
  let active = false
  let waiting = false
  let epoch = 0
  let mainSessionId

  on('session.start', async ($, e, next) => {
    endpoint = await $.env.get('WEAVE_PROVIDER_WAIT_ENDPOINT')
    const upstream = await $.env.get('ANTHROPIC_BASE_URL')
    if (!endpoint || !upstream) return next(e)
    const configured = await $.http.fetch(endpoint + '/configure', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ baseURL: upstream }),
    })
    if (!configured.ok) throw new Error('Could not configure upstream provider status')
    await $.env.set('ANTHROPIC_BASE_URL', JSON.parse(configured.text).baseURL)
    $.clock.every(100, async () => {
      if (!active) return
      const observedEpoch = epoch
      const response = await $.http.fetch(endpoint + '/status')
      if (!response.ok) throw new Error('Could not read upstream provider status')
      if (observedEpoch !== epoch) return
      const pending = JSON.parse(response.text).waiting === true
      if (pending !== waiting) {
        waiting = pending
        await $.ui.invalidate('ui.render')
      }
    })
    return next(e)
  })

  on('turn.step', async function* ($, e, next) {
    if (e.agentId || !endpoint) return yield* next(e)
    mainSessionId = await $.session.id()
    active = true
    waiting = false
    epoch++
    try {
      return yield* next(e)
    } finally {
      active = false
      waiting = false
      epoch++
      await $.ui.invalidate('ui.render')
    }
  })

  on('turn.complete', async ($, e, next) => {
    if (!e.agentId) {
      active = false
      waiting = false
      epoch++
      await $.ui.invalidate('ui.render')
    }
    return next(e)
  })

  on('session.end', async ($, e, next) => {
    active = false
    waiting = false
    epoch++
    await $.ui.invalidate('ui.render')
    return next(e)
  })

  on('ui.render', { component: 'Spinner' }, async ($, e, next) => {
    if (!active || !waiting || e.requestId !== mainSessionId) return next(e)
    return next({ ...e, props: { ...e.props, message: WAITING_MESSAGE, suffix: '' } })
  })
}
