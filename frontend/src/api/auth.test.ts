import { afterEach, describe, expect, it, vi } from 'vitest'

import { login } from './auth'

describe('login', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('solves a proof-of-work challenge and retries', async () => {
    const challenge = '0'.repeat(64)
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        error: 'proof_of_work_required',
        proof_of_work: { challenge, difficulty: 1, expires_at: Date.now() + 60_000 },
      }), { status: 429, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response('{}', { status: 200, headers: { 'X-Hephaestus-Token': 'token' } }))
    vi.stubGlobal('fetch', fetchMock)

    await login('admin', 'password')

    expect(fetchMock).toHaveBeenCalledTimes(2)
    const first = JSON.parse(fetchMock.mock.calls[0][1].body as string)
    const second = JSON.parse(fetchMock.mock.calls[1][1].body as string)
    expect(first.proof_nonce).toBeUndefined()
    expect(second.proof_nonce).toMatch(/^[0-9a-f]+$/)
    expect(second.salt).not.toBe(first.salt)
  })
})