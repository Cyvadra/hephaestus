import { describe, expect, it, vi } from 'vitest'
import { cancelSteering, getSteering, putSteering, respondToQuestions } from './client'

vi.mock('./auth', () => ({
  authFetch: vi.fn(),
}))

import { authFetch } from './auth'

describe('respondToQuestions', () => {
  it('accepts an empty successful response', async () => {
    vi.mocked(authFetch).mockResolvedValue(new Response(null, { status: 204 }))

    await expect(respondToQuestions(7, 12, [{ question_id: 'stack', selected_option_ids: ['go'] }])).resolves.toBeUndefined()
  })
})

describe('steering requests', () => {
  it('maps an empty active-run steering response to null', async () => {
    vi.mocked(authFetch).mockResolvedValue(new Response(null, { status: 204 }))

    await expect(getSteering(7)).resolves.toBeNull()
  })

  it('sends the requested mode and parses the queued instruction', async () => {
    vi.mocked(authFetch).mockResolvedValue(new Response(JSON.stringify({ status: 'queued', run_id: 12, text: 'focus', mode: 'aggressive' })))

    await expect(putSteering(7, 'focus', 'aggressive')).resolves.toEqual({ status: 'queued', run_id: 12, text: 'focus', mode: 'aggressive' })
    expect(authFetch).toHaveBeenLastCalledWith('/api/v1/sessions/7/chat-run/steering', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ text: 'focus', mode: 'aggressive' }) }))
  })

  it('cancels the replaceable instruction', async () => {
    vi.mocked(authFetch).mockResolvedValue(new Response(JSON.stringify({ status: 'cancelled' })))

    await expect(cancelSteering(7)).resolves.toEqual({ status: 'cancelled' })
    expect(authFetch).toHaveBeenLastCalledWith('/api/v1/sessions/7/chat-run/steering', { method: 'DELETE' })
  })
})