import { startChatRun, startChatRunWithFiles } from './client'
import { authFetch } from './auth'
import type { ChatRun, ChatRunDone, GenerationOptions, InteractionRequest, SendMessageResponse, Session, StreamToolCall } from './types'

export type StreamEvent =
  | { sequence: number; type: 'delta'; data: string }
  | { sequence: number; type: 'reasoning'; data: string }
  | { sequence: number; type: 'tool_call'; data: StreamToolCall }
  | { sequence: number; type: 'tool_output'; data: StreamToolCall }
  | { sequence: number; type: 'tool_result'; data: StreamToolCall }
  | { sequence: number; type: 'session_updated'; data: Session }
  | { sequence: number; type: 'ask_permission'; data: InteractionRequest }
  | { sequence: number; type: 'done'; data: ChatRunDone }
  | { sequence: number; type: 'error'; data: string }
  | { sequence: number; type: 'snapshot'; data: ChatRun }

interface EventEnvelope {
  sequence: number
  data: unknown
}

function parseEnvelope(raw: string): EventEnvelope {
  const parsed: unknown = JSON.parse(raw)
  if (
    typeof parsed !== 'object' ||
    parsed === null ||
    !Number.isSafeInteger((parsed as EventEnvelope).sequence) ||
    (parsed as EventEnvelope).sequence < 0 ||
    !('data' in parsed)
  ) {
    throw new Error('Invalid SSE event envelope')
  }
  return parsed as EventEnvelope
}

function requireString(data: unknown): string {
  if (typeof data !== 'string') throw new Error('Invalid SSE text event data')
  return data
}

function isChatRun(value: ChatRun | SendMessageResponse): value is ChatRun {
  return typeof (value as ChatRun).id === 'number'
}

// EventSource only supports GET; we need POST, so we use fetch + ReadableStream
// and parse the SSE wire format manually.
export async function* streamMessage(
  sessionId: number,
  text: string,
  activeLeafMessageId?: number | null,
  files: File[] = [],
  options: GenerationOptions = { reasoningEffort: 'high', webSearch: false },
  signal?: AbortSignal,
): AsyncGenerator<StreamEvent> {
  const run = files.length === 0
    ? await startChatRun(sessionId, 'message', text, options, undefined, activeLeafMessageId)
    : await startChatRunWithFiles(sessionId, text, files, options, activeLeafMessageId)
  if (!isChatRun(run)) {
    yield { sequence: 0, type: 'done', data: { status: 'succeeded', response: run } }
    return
  }
  yield* streamRun(run.id, signal)
}

export async function* streamRegenerate(sessionId: number, options: GenerationOptions, signal?: AbortSignal): AsyncGenerator<StreamEvent> {
  const run = await startChatRun(sessionId, 'regenerate', '', options)
  if (!isChatRun(run)) return
  yield* streamRun(run.id, signal)
}

export async function* streamContinue(sessionId: number, messageId: number, options: GenerationOptions, signal?: AbortSignal): AsyncGenerator<StreamEvent> {
  const run = await startChatRun(sessionId, 'continue', '', options, messageId)
  if (!isChatRun(run)) return
  yield* streamRun(run.id, signal)
}

export async function* streamRun(runId: number, signal?: AbortSignal): AsyncGenerator<StreamEvent> {
  yield* streamResponse(`/api/v1/chat-runs/${runId}/stream`, { signal })
}

async function* streamResponse(url: string, init: RequestInit): AsyncGenerator<StreamEvent> {
  const res = await authFetch(url, init)

  if (!res.ok || !res.body) {
    const body = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(body.error ?? res.statusText)
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  let eventName = ''
  let dataLines: string[] = []
  let expectedSequence = 0

  const dispatch = async function* (): AsyncGenerator<StreamEvent> {
    if (!eventName && dataLines.length === 0) {
      eventName = ''
      dataLines = []
      return
    }

    if (!eventName || dataLines.length === 0) {
      throw new Error('Malformed SSE event')
    }

    const envelope = parseEnvelope(dataLines.join('\n'))
    if (envelope.sequence !== expectedSequence) {
      throw new Error(`Out-of-order SSE event: expected ${expectedSequence}, received ${envelope.sequence}`)
    }
    expectedSequence++

    const progress = envelope.data as { type?: string, text?: string, tool_call?: StreamToolCall, session?: Session, interaction?: InteractionRequest }
    if (eventName === 'snapshot') {
      yield { sequence: envelope.sequence, type: 'snapshot', data: envelope.data as ChatRun }
    } else if (eventName === 'delta' || eventName === 'reasoning') {
      yield { sequence: envelope.sequence, type: eventName, data: requireString(progress.text) }
    } else if (eventName === 'tool_call' || eventName === 'tool_output' || eventName === 'tool_result') {
      if (!progress.tool_call) throw new Error('Invalid SSE tool event data')
      yield { sequence: envelope.sequence, type: eventName, data: progress.tool_call }
    } else if (eventName === 'session_updated') {
      if (!progress.session) throw new Error('Invalid SSE session event data')
      yield { sequence: envelope.sequence, type: 'session_updated', data: progress.session }
    } else if (eventName === 'ask_permission') {
      if (!progress.interaction) throw new Error('Invalid SSE interaction event data')
      yield { sequence: envelope.sequence, type: 'ask_permission', data: progress.interaction }
    } else if (eventName === 'done') {
      yield { sequence: envelope.sequence, type: 'done', data: envelope.data as ChatRunDone }
    } else {
      throw new Error(`Unknown SSE event: ${eventName}`)
    }

    eventName = ''
    dataLines = []
  }

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })

    const lines = buf.split('\n')
    buf = lines.pop() ?? ''

    for (const line of lines) {
      const normalized = line.endsWith('\r') ? line.slice(0, -1) : line

      if (normalized === '') {
        for await (const ev of dispatch()) {
          yield ev
        }
        continue
      }

      if (normalized.startsWith(':')) {
        continue
      }

      if (normalized.startsWith('event:')) {
        eventName = normalized.slice(6).trim()
      } else if (normalized.startsWith('data:')) {
        let chunk = normalized.slice(5)
        if (chunk.startsWith(' ')) {
          chunk = chunk.slice(1)
        }
        dataLines.push(chunk)
      }
    }
  }

  for await (const ev of dispatch()) {
    yield ev
  }
}
