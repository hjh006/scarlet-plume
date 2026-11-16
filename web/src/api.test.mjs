import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createServer } from 'node:http'
import { once } from 'node:events'
import { ApiError, describeError, request, retryAfterSeconds } from './api.ts'

test('rate limits retain bounded Retry-After and safe actionable text', () => {
  const now = Date.parse('2026-11-16T00:00:00Z')
  assert.equal(retryAfterSeconds('12', now), 12)
  assert.equal(retryAfterSeconds('Mon, 16 Nov 2026 00:00:30 GMT', now), 30)
  assert.equal(retryAfterSeconds('999999999999999', now), 86400)
  for (const value of [null, '', '-1', 'invalid', 'Sun, 15 Nov 2026 00:00:00 GMT']) assert.equal(retryAfterSeconds(value, now), undefined)
  assert.match(describeError(new ApiError(429, '', 12)), /12 秒/)
  assert.match(describeError(new DOMException('', 'TimeoutError')), /核对结果/)
})

test('HTTP requests retain cancellation and structured failure status', async () => {
  const server = createServer((req, res) => {
    if (req.url === '/slow') return
    if (req.url === '/lease-conflict') {
      res.writeHead(409, { 'Content-Type': 'application/json' })
      return res.end(JSON.stringify({error:'仍有未决步骤',code:'inflight_unresolved',blockers:[{stepId:'step-1',message:'保留未知执行结果'}],recovery:['核对原调用审计']}))
    }
    res.writeHead(429, { 'Content-Type': 'application/json', 'Retry-After': '9' })
    res.end(JSON.stringify({ error: 'retry later' }))
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  try {
    const url = `http://127.0.0.1:${server.address().port}`
    await assert.rejects(request(url), e => e instanceof ApiError && e.status === 429 && e.retryAfterSeconds === 9)
    await assert.rejects(request(url+'/lease-conflict'),e=>e instanceof ApiError && e.status===409 && e.details.code==='inflight_unresolved' && e.details.blockers[0].stepId==='step-1')
    await assert.rejects(request(url + '/slow', { signal: AbortSignal.timeout(30) }), e => e.name === 'TimeoutError')
  } finally { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)) }
})
