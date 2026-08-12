import type { WorkflowDelta, WorkflowProgressEnvelope, WorkflowRun, WorkflowStepRun } from './types'

// subscribeWorkflowRun 通过 SSE 订阅一次工作流运行的实时进度。返回用于取消
// 订阅的清理函数。
//
// 服务端统一发送 ProgressEvent 信封（快照与实时事件同构），并在运行到达终态
// 后发出 done 事件、短暂保持连接；本客户端收到 done 或连接被关闭时都会主动
// close()，从而阻止 EventSource 自动重连造成反复请求。
export interface WorkflowRunStreamHandlers {
  onRun: (run: WorkflowRun) => void
  onStep: (step: WorkflowStepRun) => void
  onDelta: (delta: WorkflowDelta, step?: WorkflowStepRun) => void
  onDone: (final?: WorkflowRun) => void
  // onEnd 在流被关闭但未收到 done 时触发（例如网络中断或运行不存在），
  // 调用方应刷新运行列表并按需重新订阅。
  onEnd?: () => void
}

function parseEnvelope(raw: string): WorkflowProgressEnvelope | null {
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null) return null
    const envelope = parsed as WorkflowProgressEnvelope
    if (!Number.isSafeInteger(envelope.sequence) || envelope.data == null) return null
    return envelope
  } catch {
    return null
  }
}

// progressOf 返回 data 中包含 key 字段的 ProgressEvent，否则返回 null。
function progressOf(message: MessageEvent, key: 'run' | 'step' | 'delta'): Record<string, unknown> | null {
  const envelope = parseEnvelope(message.data)
  if (!envelope || typeof envelope.data !== 'object' || envelope.data === null) return null
  const data = envelope.data as unknown as Record<string, unknown>
  return key in data ? data : null
}

export function subscribeWorkflowRun(runID: number, handlers: WorkflowRunStreamHandlers): () => void {
  const source = new EventSource(`/api/v1/workflow-runs/${runID}/stream`)

  source.addEventListener('run', (message) => {
    const data = progressOf(message as MessageEvent, 'run')
    if (data?.run) handlers.onRun(data.run as WorkflowRun)
  })

  source.addEventListener('step', (message) => {
    const data = progressOf(message as MessageEvent, 'step')
    if (data?.step) handlers.onStep(data.step as WorkflowStepRun)
  })

  source.addEventListener('delta', (message) => {
    const data = progressOf(message as MessageEvent, 'delta')
    if (data?.delta) handlers.onDelta(data.delta as WorkflowDelta, data.step as WorkflowStepRun | undefined)
  })

  source.addEventListener('done', (message) => {
    const data = progressOf(message as MessageEvent, 'run')
    handlers.onDone(data?.run as WorkflowRun | undefined)
    source.close()
  })

  source.addEventListener('error', () => {
    // 服务端在发出 done 后（或运行不存在时）会关闭连接；这里主动关闭，
    // 阻止 EventSource 自动重连造成反复请求。若未收到 done，则通过 onEnd
    // 通知调用方刷新并按需重新订阅。
    source.close()
    handlers.onEnd?.()
  })

  return () => source.close()
}
