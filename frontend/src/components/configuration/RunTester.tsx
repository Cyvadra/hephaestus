import { AlertCircle, ChevronRight, LoaderCircle, Play, RefreshCw, Square } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import {
  cancelJobRun,
  cancelWorkflowRun,
  getJobRun,
  getWorkflowRun,
  listJobRuns,
  listProjects,
  listWorkflowRuns,
  startWorkflowRun,
} from '../../api/client'
import { subscribeWorkflowRun } from '../../api/workflowStream'
import type { JobRun, JobRunDetail, WorkflowRun, WorkflowRunDetail, WorkflowStepRun } from '../../api/types'

const STATUS_LABELS: Record<string, string> = {
  pending: '等待',
  running: '运行中',
  succeeded: '成功',
  failed: '失败',
  fatal: '致命错误',
  cancelled: '已取消',
  interrupted: '已中断',
  completed_with_errors: '部分成功',
}

function RunStatusBadge({ status }: { status: string }) {
  return <span className={`run-status run-status-${status}`}>{STATUS_LABELS[status] ?? status}</span>
}

function isActive(status: string): boolean {
  return status === 'pending' || status === 'running'
}

function formatTime(value: string | null | undefined): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function parseJSONLoose(source: string): unknown | undefined {
  try {
    return JSON.parse(source) as unknown
  } catch {
    return undefined
  }
}

function prettyJSON(value: unknown): string {
  if (value === undefined || value === null) return ''
  return JSON.stringify(value, null, 2)
}

function sampleFromSchema(schema: unknown): Record<string, unknown> {
  if (!schema || typeof schema !== 'object') return {}
  const raw = schema as Record<string, unknown>
  const properties = raw.properties && typeof raw.properties === 'object' ? raw.properties as Record<string, unknown> : {}
  const required = Array.isArray(raw.required) ? raw.required.filter(item => typeof item === 'string') as string[] : []
  const result: Record<string, unknown> = {}
  for (const key of required) if (key in properties) result[key] = sampleValue(properties[key])
  return result
}

function sampleValue(schema: unknown): unknown {
  if (!schema || typeof schema !== 'object') return ''
  const raw = schema as Record<string, unknown>
  switch (raw.type) {
    case 'number':
    case 'integer':
      return 0
    case 'boolean':
      return false
    case 'array':
      return []
    case 'object': {
      const props = raw.properties && typeof raw.properties === 'object' ? raw.properties as Record<string, unknown> : {}
      const out: Record<string, unknown> = {}
      for (const [key, value] of Object.entries(props)) out[key] = sampleValue(value)
      return out
    }
    default:
      return ''
  }
}

function ErrorLine({ message }: { message: string }) {
  if (!message) return null
  return <div className="run-error"><AlertCircle size={13} />{message}</div>
}

interface LiveProgress {
  runID: number
  status: string
  stepIndex: number
  stepText: string
  text: string
  tools: string[]
}

// WorkflowRunTester 提供工作流的在线测试：填写输入、选择 Project 启动运行，
// 通过 SSE 实时查看步骤输出与结果，可取消进行中的运行。
export function WorkflowRunTester({ workflowName, inputSchema }: { workflowName: string; inputSchema: unknown }) {
  const [projects, setProjects] = useState<string[]>([])
  const [project, setProject] = useState('')
  const [inputSource, setInputSource] = useState('')
  const [runs, setRuns] = useState<WorkflowRun[]>([])
  const [details, setDetails] = useState<Record<number, WorkflowRunDetail | null>>({})
  const [live, setLive] = useState<LiveProgress | null>(null)
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState('')
  const [refreshKey, setRefreshKey] = useState(0)
  const [streamKey, setStreamKey] = useState(0)

  useEffect(() => {
    void listProjects().then(list => setProjects(list.map(item => item.Name))).catch(() => undefined)
    setInputSource(JSON.stringify(sampleFromSchema(inputSchema), null, 2))
    setProject('')
  }, [workflowName, inputSchema])

  const load = useCallback(() => {
    void listWorkflowRuns(workflowName, 20).then(setRuns).catch(reason => setError(reason instanceof Error ? reason.message : '加载运行记录失败'))
  }, [workflowName])
  useEffect(() => { load() }, [load, refreshKey])

  // 通过 SSE 实时订阅最新一条进行中的运行，替代轮询；运行到达终态后由
  // subscribeWorkflowRun 关闭连接并在 onDone 里刷新最终记录。
  const newestID = runs[0]?.ID
  const newestStatus = runs[0]?.Status
  useEffect(() => {
    if (!newestID || !isActive(newestStatus)) return
    return subscribeWorkflowRun(newestID, {
      onRun: (run) => {
        setRuns(current => current.map(item => item.ID === run.ID ? run : item))
        // 始终为被订阅的运行建立详情（保留已积累的 steps），使步骤实时可见。
        setDetails(current => ({ ...current, [run.ID]: { run, steps: current[run.ID]?.steps ?? [] } }))
        // 快照事件到达时初始化实时面板（若尚未建立），并同步最新状态。
        setLive(current => current
          ? { ...current, runID: run.ID, status: run.Status }
          : { runID: run.ID, status: run.Status, stepIndex: 0, stepText: '', text: '', tools: [] })
      },
      onStep: (step) => {
        setDetails(current => {
          const existing = current[step.WorkflowRunID]
          if (!existing) return current
          const steps = existing.steps.some(item => item.ID === step.ID)
            ? existing.steps.map(item => item.ID === step.ID ? step : item)
            : [...existing.steps, step]
          return { ...current, [step.WorkflowRunID]: { ...existing, steps } }
        })
        setLive(current => current && current.runID === step.WorkflowRunID
          ? { ...current, stepIndex: step.Index, stepText: step.Text }
          : current)
      },
      onDelta: (delta, step) => {
        setLive(current => {
          if (!current) return current
          if (step && current.runID !== step.WorkflowRunID) return current
          if (delta.type === 'delta' || delta.type === 'reasoning') {
            return { ...current, text: current.text + (delta.text ?? '') }
          }
          if (delta.type === 'tool_call' && delta.tool_call?.name) {
            return { ...current, tools: [...current.tools, delta.tool_call.name] }
          }
          return current
        })
      },
      onDone: () => {
        setLive(null)
        void load()
      },
      onEnd: () => {
        // 流被关闭但未收到 done（网络中断等）：刷新列表，若运行仍在进行则
        // 通过 streamKey 重新订阅。
        void load()
        setStreamKey(key => key + 1)
      },
    })
  }, [newestID, newestStatus, load, streamKey])

  const start = async () => {
    setError('')
    setLive(null)
    const parsed = parseJSONLoose(inputSource)
    if (parsed === undefined || parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
      setError('输入必须是合法的 JSON 对象')
      return
    }
    setStarting(true)
    try {
      const run = await startWorkflowRun(workflowName, project.trim() || 'default-workspace', parsed as Record<string, unknown>)
      setRuns(current => [run, ...current.filter(item => item.ID !== run.ID)])
      setDetails(current => ({ ...current, [run.ID]: { run, steps: [] } }))
      setRefreshKey(key => key + 1)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '启动失败')
    } finally {
      setStarting(false)
    }
  }

  const toggle = async (runID: number) => {
    if (details[runID]) {
      setDetails(current => ({ ...current, [runID]: null }))
      return
    }
    try {
      const detail = await getWorkflowRun(runID)
      setDetails(current => ({ ...current, [runID]: detail }))
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '加载详情失败')
    }
  }

  const cancel = async (runID: number) => {
    try {
      await cancelWorkflowRun(runID)
      setRefreshKey(key => key + 1)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '取消失败')
    }
  }

  return (
    <section className="run-tester">
      <header><h2>在线测试</h2><p>填写输入并启动一次该工作流的运行，实时查看每个步骤的执行结果。</p></header>
      <div className="run-tester-form">
        <label className="run-tester-project">Project
          <input list="run-project-suggestions" placeholder="default-workspace" value={project} onChange={event => setProject(event.target.value)} />
          <datalist id="run-project-suggestions">{projects.map(item => <option key={item} value={item} />)}</datalist>
        </label>
        <label className="run-tester-input">输入 JSON <small>已按 input_schema 预填</small>
          <textarea className="configuration-json-editor" rows={7} value={inputSource} onChange={event => setInputSource(event.target.value)} />
        </label>
        <div className="run-tester-actions">
          <button className="primary" type="button" disabled={starting} onClick={() => void start()}>{starting ? <LoaderCircle className="spin" size={15} /> : <Play size={15} />}启动运行</button>
          <button type="button" onClick={() => setRefreshKey(key => key + 1)}><RefreshCw size={15} />刷新</button>
        </div>
        {error && <div className="configuration-alert error" role="alert"><AlertCircle size={16} /><span>{error}</span></div>}
      </div>
      {live && (
        <div className="run-live">
          <header>
            <span className="run-live-badge"><LoaderCircle className="spin" size={13} />运行中 #{live.runID}</span>
            <span className="run-live-step">{live.stepText ? `步骤 ${live.stepIndex + 1}：${live.stepText}` : '准备中…'}</span>
          </header>
          {live.tools.length > 0 && (
            <div className="run-live-tools">{live.tools.map((name, index) => <span key={index} className="run-live-tool">{name}</span>)}</div>
          )}
          {live.text && <pre className="run-live-text">{live.text}</pre>}
        </div>
      )}
      <RunList<WorkflowRun, WorkflowRunDetail>
        runs={runs}
        details={details}
        empty="还没有运行记录，点击上方「启动运行」开始测试。"
        onToggle={toggle}
        onCancel={cancel}
        runTitle={run => run.WorkflowName}
        runSubtitle={run => `#${run.ID} · 尝试 ${run.Attempt} · ${run.ProjectName}`}
        runInputText={run => prettyJSON(run.Input)}
        runOutputText={run => prettyJSON(run.Output)}
        renderSteps={detail => (detail ? detail.steps : [])}
      />
    </section>
  )
}

// JobRunsPanel 展示一个 Job 的历史调度运行记录，可展开查看其绑定的工作流尝试。
export function JobRunsPanel({ jobName }: { jobName: string }) {
  const [runs, setRuns] = useState<JobRun[]>([])
  const [details, setDetails] = useState<Record<number, JobRunDetail | null>>({})
  const [error, setError] = useState('')
  const [refreshKey, setRefreshKey] = useState(0)

  const load = useCallback(() => {
    void listJobRuns(jobName, 20).then(setRuns).catch(reason => setError(reason instanceof Error ? reason.message : '加载运行记录失败'))
  }, [jobName])
  useEffect(() => { load() }, [load, refreshKey])

  const newestID = runs[0]?.ID
  const newestStatus = runs[0]?.Status
  useEffect(() => {
    if (!newestID || !isActive(newestStatus)) return
    const timer = window.setInterval(() => {
      void getJobRun(newestID).then(detail => {
        setRuns(current => current.map(run => run.ID === detail.run.ID ? detail.run : run))
        setDetails(current => ({ ...current, [detail.run.ID]: detail }))
      }).catch(() => undefined)
    }, 1500)
    return () => window.clearInterval(timer)
  }, [newestID, newestStatus])

  const toggle = async (runID: number) => {
    if (details[runID]) {
      setDetails(current => ({ ...current, [runID]: null }))
      return
    }
    try {
      const detail = await getJobRun(runID)
      setDetails(current => ({ ...current, [runID]: detail }))
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '加载详情失败')
    }
  }

  const cancel = async (runID: number) => {
    try {
      await cancelJobRun(runID)
      setRefreshKey(key => key + 1)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '取消失败')
    }
  }

  return (
    <section className="run-tester">
      <header><h2>运行记录</h2><p>该 Job 由调度器按触发器自动执行；此处仅查看与取消，不支持手动触发。</p></header>
      {error && <div className="configuration-alert error" role="alert"><AlertCircle size={16} /><span>{error}</span></div>}
      <RunList<JobRun, JobRunDetail>
        runs={runs}
        details={details}
        empty="该任务还没有运行记录。"
        onToggle={toggle}
        onCancel={cancel}
        runTitle={run => run.JobName}
        runSubtitle={run => `#${run.ID} · ${run.LocalDate}`}
        runInputText={() => ''}
        runOutputText={() => ''}
        renderSteps={detail => (detail ? detail.workflow_runs : [])}
      />
    </section>
  )
}

interface RunListProps<R extends WorkflowRun | JobRun, D extends WorkflowRunDetail | JobRunDetail> {
  runs: R[]
  details: Record<number, D | null>
  empty: string
  onToggle: (runID: number) => void
  onCancel: (runID: number) => void
  runTitle: (run: R) => string
  runSubtitle: (run: R) => string
  runInputText: (run: R) => string
  runOutputText: (run: R) => string
  renderSteps: (detail: D | null) => Array<WorkflowStepRun | WorkflowRun>
}

function RunList<R extends WorkflowRun | JobRun, D extends WorkflowRunDetail | JobRunDetail>({ runs, details, empty, onToggle, onCancel, runTitle, runSubtitle, runInputText, runOutputText, renderSteps }: RunListProps<R, D>) {
  if (runs.length === 0) return <div className="run-list-empty">{empty}</div>
  return (
    <div className="run-list">
      {runs.map(run => {
        const detail = details[run.ID] ?? null
        const expanded = Boolean(detail)
        const active = isActive(run.Status)
        return (
          <article className={`run-row${expanded ? ' expanded' : ''}`} key={run.ID}>
            <div className="run-row-head">
              <button className="run-row-toggle" type="button" aria-expanded={expanded} onClick={() => onToggle(run.ID)}><ChevronRight size={15} /></button>
              <RunStatusBadge status={run.Status} />
              <div className="run-row-title"><strong>{runTitle(run)}</strong><span>{runSubtitle(run)}</span></div>
              <div className="run-row-time">{formatTime(run.StartedAt)} → {formatTime(run.FinishedAt)}</div>
              {active && <button className="run-cancel" type="button" title="取消运行" onClick={() => onCancel(run.ID)}><Square size={13} />取消</button>}
            </div>
            {run.Error && <ErrorLine message={run.Error} />}
            {expanded && detail && (
              <div className="run-detail">
                <RunData inputText={runInputText(run)} outputText={runOutputText(run)} />
                <div className="run-steps">
                  {renderSteps(detail).length === 0 && <span className="run-detail-muted">没有步骤记录</span>}
                  {renderSteps(detail).map((step, index) => <WorkflowStepRow key={step.ID} index={index} step={step} />)}
                </div>
              </div>
            )}
          </article>
        )
      })}
    </div>
  )
}

function RunData({ inputText, outputText }: { inputText: string; outputText: string }) {
  if (!inputText && !outputText) return null
  return (
    <div className="run-data">
      {inputText && <div><h4>输入</h4><pre>{inputText}</pre></div>}
      {outputText && <div><h4>输出</h4><pre>{outputText}</pre></div>}
    </div>
  )
}

function isWorkflowStepRun(step: WorkflowStepRun | WorkflowRun): step is WorkflowStepRun {
  return (step as WorkflowStepRun).Index !== undefined
}

function WorkflowStepRow({ index, step }: { index: number; step: WorkflowStepRun | WorkflowRun }) {
  const stepRun = isWorkflowStepRun(step)
  const title = stepRun ? `步骤 ${index + 1}` : `工作流 ${step.WorkflowName} · 尝试 ${(step as WorkflowRun).Attempt}`
  const text = stepRun ? step.Text : ''
  const output = stepRun ? step.Output : prettyJSON((step as WorkflowRun).Output)
  return (
    <div className="run-step-row">
      <RunStatusBadge status={step.Status} />
      <div className="run-step-body">
        <strong>{title}</strong>
        {text && <p>{text}</p>}
        {step.Error && <ErrorLine message={step.Error} />}
        {output && <pre>{output}</pre>}
      </div>
    </div>
  )
}
