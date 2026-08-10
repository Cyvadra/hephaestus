import type { InteractionRequest, SendMessageResponse, Session, StreamToolCall } from './types'

export type StreamEvent =
  | { sequence: number; type: 'delta'; data: string }
  | { sequence: number; type: 'reasoning'; data: string }
  | { sequence: number; type: 'tool_call'; data: StreamToolCall }
  | { sequence: number; type: 'tool_output'; data: StreamToolCall }
  | { sequence: number; type: 'tool_result'; data: StreamToolCall }
  | { sequence: number; type: 'session_updated'; data: Session }
  | { sequence: number; type: 'ask_permission'; data: InteractionRequest }
  | { sequence: number; type: 'done'; data: SendMessageResponse }
  | { sequence: number; type: 'error'; data: string }

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
    (parsed as EventEnvelope).sequence < 1 ||
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

// EventSource only supports GET; we need POST, so we use fetch + ReadableStream
// and parse the SSE wire format manually.
export async function* streamMessage(
  sessionId: number,
  text: string,
  activeLeafMessageId?: number,
  signal?: AbortSignal,
): AsyncGenerator<StreamEvent> {
  yield* streamResponse(`/api/v1/sessions/${sessionId}/messages/stream`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text, active_leaf_message_id: activeLeafMessageId }),
    signal,
  })
}

export async function* streamRegenerate(sessionId: number, signal?: AbortSignal): AsyncGenerator<StreamEvent> {
  yield* streamResponse(`/api/v1/sessions/${sessionId}/regenerate/stream`, {
    method: 'POST',
    signal,
  })
}

export async function* streamContinue(sessionId: number, messageId: number, signal?: AbortSignal): AsyncGenerator<StreamEvent> {
   yield* streamResponse(`/api/v1/sessions/${sessionId}/messages/${messageId}/continue/stream`, {
    method: 'POST',
    signal,
  })
}

async function* streamResponse(url: string, init: RequestInit): AsyncGenerator<StreamEvent> {
  const res = await fetch(url, init)

  if (!res.ok || !res.body) {
    const body = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(body.error ?? res.statusText)
  }

  const contentType = res.headers.get('content-type') ?? ''
  if (!contentType.includes('text/event-stream')) {
    const payload: SendMessageResponse = await res.json()
    yield { sequence: 0, type: 'done', data: payload }
    return
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  let eventName = ''
  let dataLines: string[] = []
  let expectedSequence = 1

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

    if (eventName === 'delta') {
      yield { sequence: envelope.sequence, type: 'delta', data: requireString(envelope.data) }
    } else if (eventName === 'reasoning') {
      yield { sequence: envelope.sequence, type: 'reasoning', data: requireString(envelope.data) }
    } else if (eventName === 'tool_call' || eventName === 'tool_output' || eventName === 'tool_result') {
      yield { sequence: envelope.sequence, type: eventName, data: envelope.data as StreamToolCall }
    } else if (eventName === 'session_updated') {
      yield { sequence: envelope.sequence, type: 'session_updated', data: envelope.data as Session }
	} else if (eventName === 'ask_permission') {
	  yield { sequence: envelope.sequence, type: 'ask_permission', data: envelope.data as InteractionRequest }
    } else if (eventName === 'done') {
      yield { sequence: envelope.sequence, type: 'done', data: envelope.data as SendMessageResponse }
    } else if (eventName === 'error') {
      yield { sequence: envelope.sequence, type: 'error', data: requireString(envelope.data) }
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
