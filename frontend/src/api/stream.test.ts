import { beforeEach, describe, expect, it, vi } from 'vitest'
import { startBtwQuestion, startChatRun } from './client'
import { streamBtwQuestion, streamContinue } from './stream'

vi.mock('./client', () => ({
  startChatRun: vi.fn(),
  startChatRunWithFiles: vi.fn(),
  startBtwQuestion: vi.fn(),
  ensureOk: async (res: Response) => {
    if (res.ok) return res
    throw new Error(res.statusText)
  },
}))

function sseResponse(events: string[]): Response {
  return new Response(events.join(''), {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream' },
  })
}

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

describe('streamBtwQuestion', () => {
  beforeEach(() => vi.mocked(startBtwQuestion).mockReset())

  it('forwards the question and selected leaf', async () => {
    vi.mocked(startBtwQuestion).mockResolvedValue(
      sseResponse([
        'event: delta\ndata: {"sequence":0,"data":{"text":"side "}}\n\n',
        'event: delta\ndata: {"sequence":1,"data":{"text":"answer"}}\n\n',
        'event: done\ndata: {"sequence":2,"data":{"status":"succeeded"}}\n\n',
      ]),
    )

    const events = []
    for await (const event of streamBtwQuestion(7, 'why?', 42)) events.push(event)

    expect(startBtwQuestion).toHaveBeenCalledWith(7, 'why?', 42, undefined)
    expect(events).toEqual([
      { sequence: 0, type: 'delta', data: 'side ' },
      { sequence: 1, type: 'delta', data: 'answer' },
      { sequence: 2, type: 'done' },
    ])
  })

  it('surfaces an error event as a string', async () => {
    vi.mocked(startBtwQuestion).mockResolvedValue(
      sseResponse(['event: error\ndata: {"sequence":0,"data":"model unavailable"}\n\n']),
    )

    const events = []
    for await (const event of streamBtwQuestion(7, 'why?', null)) events.push(event)

    expect(startBtwQuestion).toHaveBeenCalledWith(7, 'why?', null, undefined)
    expect(events).toEqual([{ sequence: 0, type: 'error', data: 'model unavailable' }])
  })

  it('rejects out-of-order sequences', async () => {
    vi.mocked(startBtwQuestion).mockResolvedValue(
      sseResponse(['event: delta\ndata: {"sequence":1,"data":{"text":"late"}}\n\n']),
    )

    const consume = async () => {
      const events = []
      for await (const event of streamBtwQuestion(7, 'why?', 42)) events.push(event)
      return events
    }
    await expect(consume()).rejects.toThrow('Out-of-order SSE event')
  })
})
