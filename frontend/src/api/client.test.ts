import { describe, expect, it, vi } from 'vitest'
import { respondToQuestions } from './client'

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