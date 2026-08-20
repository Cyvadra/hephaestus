import { beforeEach, describe, expect, it, vi } from 'vitest'
import { startChatRun } from './client'
import { streamContinue } from './stream'

vi.mock('./client', () => ({
  startChatRun: vi.fn(),
  startChatRunWithFiles: vi.fn(),
}))

describe('streamContinue', () => {
  beforeEach(() => vi.mocked(startChatRun).mockReset())

  it('forwards the selected generation options', async () => {
    vi.mocked(startChatRun).mockResolvedValue({ command_response: 'done' })
    const options = { reasoningEffort: 'max' as const, webSearch: true }

    const events = []
    for await (const event of streamContinue(7, 42, options)) events.push(event)

    expect(startChatRun).toHaveBeenCalledWith(7, 'continue', '', options, 42)
    expect(events).toEqual([])
  })
})