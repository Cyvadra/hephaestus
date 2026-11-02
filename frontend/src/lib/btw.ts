export interface BtwAnswer {
  id: number
  sessionId: number
  question: string
  text: string
  status: 'streaming' | 'done' | 'error'
  error?: string
  /** Last completed assistant message the answer is visually attached to. */
  anchorMessageId: number | null
}

/**
 * Returns the question text when text is a /btw command, an empty string for
 * a bare /btw, and null for anything else. /btwx and /btwq are not commands.
 */
export function parseBtwCommand(text: string): string | null {
  const trimmed = text.trimStart()
  if (!trimmed.startsWith('/btw')) return null
  const rest = trimmed.slice(4)
  if (rest !== '' && !/^\s/.test(rest)) return null
  return rest.trim()
}
