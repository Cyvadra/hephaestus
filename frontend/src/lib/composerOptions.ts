import type { ConciergeItem, GenerationOptions, ReasoningEffort, Session } from '../api/types'

// composerOptions resolves the composer's generation controls (reasoning effort
// and 联网) for both a draft session and a loaded session, and remembers 联网
// per Concierge so a new session starts from that Concierge's own defaults
// instead of whatever the previously viewed session happened to use.

/** Tool group whose web_search/web_fetch tools the 联网 control gates. */
export const WEB_TOOL_GROUP = 'web'

/** localStorage prefix holding the last 联网 choice made for one Concierge. */
export const WEB_SEARCH_STORAGE_KEY_PREFIX = 'hephaestus.webSearch.'

/** Storage for browser-only code paths; tests may run without one. */
function browserStorage(): Storage | null {
  return typeof localStorage === 'undefined' ? null : localStorage
}

/**
 * Normalizes an Identity or Session reasoning effort onto the four composer
 * choices. Unknown and empty values collapse to "none", which is also how the
 * server reads an unset session effort.
 */
export function composerReasoningEffort(effort: string | null | undefined): ReasoningEffort {
  return effort === 'low' || effort === 'high' || effort === 'max' ? effort : 'none'
}

/** Reports whether a tool-group list exposes the group the 联网 control gates. */
export function supportsWebSearch(toolGroups: readonly string[] | null | undefined): boolean {
  return (toolGroups ?? []).includes(WEB_TOOL_GROUP)
}

/**
 * Reports whether 联网 can be switched on for this composer at all. The web tool
 * group has to be within reach: either the Concierge offers it, in which case a
 * draft or live session may activate it, or the session already carries it.
 */
export function webSearchSelectable(
  concierge: { tool_groups?: readonly string[] } | null | undefined,
  sessionToolGroups?: readonly string[] | null,
): boolean {
  return supportsWebSearch(concierge?.tool_groups) || supportsWebSearch(sessionToolGroups)
}

/**
 * The composer's authorization switch. Only two states are supported: ask for
 * every sensitive action, or allow everything for the current session.
 */
export type AuthorizationMode = 'askEachTime' | 'allowAll'

/** Flips the authorization switch, whose highlighted state means "allow all". */
export function toggleAuthorizationMode(mode: AuthorizationMode): AuthorizationMode {
  return mode === 'allowAll' ? 'askEachTime' : 'allowAll'
}

/**
 * The reasoning label is a thinking switch, not a level cycler: any effort but
 * 即答 (none) means thinking is on, so a click turns it off, and a click on 即答
 * turns it back on at 深度. 适度 and 极度 stay in the label's hover menu.
 */
export function toggleReasoningEffort(current: ReasoningEffort): ReasoningEffort {
  return current === 'none' ? 'high' : 'none'
}

/** Storage key of the 联网 choice cached for one Concierge. */
export function webSearchStorageKey(concierge: string): string {
  return `${WEB_SEARCH_STORAGE_KEY_PREFIX}${concierge}`
}

/** Returns the 联网 choice last remembered for concierge, or null when unset. */
export function readWebSearchPreference(concierge: string, storage: Storage | null = browserStorage()): boolean | null {
  const stored = storage?.getItem(webSearchStorageKey(concierge))
  return stored === 'true' ? true : stored === 'false' ? false : null
}

/** Remembers the 联网 choice for concierge so its next session reuses it. */
export function rememberWebSearchPreference(concierge: string, enabled: boolean, storage: Storage | null = browserStorage()): void {
  storage?.setItem(webSearchStorageKey(concierge), String(enabled))
}

type ConciergeDefaults = Pick<ConciergeItem, 'name' | 'reasoning_effort' | 'default_tool_groups'>

/**
 * Composer defaults for a session being started from a Concierge: the
 * Identity's reasoning effort, plus the 联网 state remembered for that
 * Concierge. Concierges that do not enable the web tool group by default
 * cannot search the web at all, so their sessions always start with 联网 off.
 * The others default to on, matching the backend's decision for a new session.
 */
export function conciergeComposerOptions(concierge: ConciergeDefaults, storage: Storage | null = browserStorage()): GenerationOptions {
  return {
    reasoningEffort: composerReasoningEffort(concierge.reasoning_effort),
    webSearch: supportsWebSearch(concierge.default_tool_groups)
      ? readWebSearchPreference(concierge.name, storage) ?? true
      : false,
  }
}

type SessionOptionOverrides = { reasoningEffort?: string; enableWebSearch?: boolean }

/**
 * Reports the session patch needed when a draft's composer options diverge
 * from the Identity-derived values the backend persisted at creation, or null
 * when the new session already matches them. Returning null keeps the Identity
 * default in place instead of rewriting it as an explicit session value.
 */
export function sessionOptionOverrides(
  session: Pick<Session, 'ReasoningEffort' | 'EnableWebSearch'>,
  options: GenerationOptions,
): SessionOptionOverrides | null {
  const overrides: SessionOptionOverrides = {}
  if (composerReasoningEffort(session.ReasoningEffort) !== options.reasoningEffort) {
    overrides.reasoningEffort = options.reasoningEffort
  }
  // A nil EnableWebSearch means "not yet set", which clients treat as enabled.
  if ((session.EnableWebSearch ?? true) !== options.webSearch) {
    overrides.enableWebSearch = options.webSearch
  }
  return Object.keys(overrides).length === 0 ? null : overrides
}
