import type { SendMessageResponse } from './types'

export type StreamEvent =
  | { type: 'delta'; data: string }
  | { type: 'done'; data: SendMessageResponse }
  | { type: 'error'; data: string }

function parseJSONOrRawString(raw: string): string {
  try {
    const parsed: unknown = JSON.parse(raw)
    return typeof parsed === 'string' ? parsed : String(parsed)
  } catch {
    return raw
  }
}

// EventSource only supports GET; we need POST, so we use fetch + ReadableStream
// and parse the SSE wire format manually.
export async function* streamMessage(
  sessionId: number,
  text: string,
  activeLeafMessageId?: number,
): AsyncGenerator<StreamEvent> {
  const res = await fetch(`/api/v1/sessions/${sessionId}/messages/stream`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text, active_leaf_message_id: activeLeafMessageId }),
  })

  if (!res.ok || !res.body) {
    const body = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(body.error ?? res.statusText)
  }

  const contentType = res.headers.get('content-type') ?? ''
  if (!contentType.includes('text/event-stream')) {
    const payload: SendMessageResponse = await res.json()
    yield { type: 'done', data: payload }
    return
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  let eventName = ''
  let dataLines: string[] = []

  const dispatch = async function* (): AsyncGenerator<StreamEvent> {
    if (!eventName || dataLines.length === 0) {
      eventName = ''
      dataLines = []
      return
    }

    const raw = dataLines.join('\n')

    if (eventName === 'delta') {
      yield { type: 'delta', data: parseJSONOrRawString(raw) }
    } else if (eventName === 'done') {
      const payload: SendMessageResponse = JSON.parse(raw)
      yield { type: 'done', data: payload }
    } else if (eventName === 'error') {
      yield { type: 'error', data: parseJSONOrRawString(raw) }
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
