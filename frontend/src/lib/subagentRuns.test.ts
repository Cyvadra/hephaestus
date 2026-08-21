import { describe, expect, it } from 'vitest'
import type { Session, SubagentRunSummary } from '../api/types'
import { sessionSubagentRuns, subagentSessionTarget, subagentStatusKey } from './subagentRuns'

const session = (runs?: SubagentRunSummary[]) => ({ subagent_runs: runs } as Session)

describe('subagent run sidebar helpers', () => {
  it('treats a missing or empty run list as not expandable', () => {
    expect(sessionSubagentRuns(session())).toEqual([])
    expect(sessionSubagentRuns(session([]))).toEqual([])
  })

  it('uses child_session_id as the navigation boundary', () => {
    const running = { id: 1, status: 'running', label: 'work', created_at: '' } as SubagentRunSummary
    const ready = { ...running, child_session_id: 42 }
    expect(subagentSessionTarget(running)).toBeNull()
    expect(subagentSessionTarget(ready)).toBe(42)
  })

  it('maps statuses to the existing run translation namespace', () => {
    expect(subagentStatusKey('interrupted')).toBe('configuration.runs.interrupted')
  })
})