import type { Session, SubagentRunStatus, SubagentRunSummary } from '../api/types'

export const sessionSubagentRuns = (session: Session): SubagentRunSummary[] =>
  session.subagent_runs ?? []

export const subagentSessionTarget = (run: SubagentRunSummary): number | null =>
  run.child_session_id ?? null

export const subagentStatusKey = (status: SubagentRunStatus): `configuration.runs.${SubagentRunStatus}` =>
  `configuration.runs.${status}`

/** Reports whether a run has stopped for good and can no longer be steered. */
export const isTerminalSubagentStatus = (status: SubagentRunStatus): boolean =>
  status !== 'pending' && status !== 'running'
