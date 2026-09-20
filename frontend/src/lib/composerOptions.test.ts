import { describe, expect, it } from 'vitest'
import type { ConciergeItem } from '../api/types'
import { composerReasoningEffort, conciergeComposerOptions, readWebSearchPreference, rememberWebSearchPreference, sessionOptionOverrides, supportsWebSearch, toggleAuthorizationMode, toggleReasoningEffort, webSearchSelectable, webSearchStorageKey } from './composerOptions'

function fakeStorage(initial: Record<string, string> = {}): Storage {
  const entries = new Map(Object.entries(initial))
  return {
    get length() { return entries.size },
    clear: () => entries.clear(),
    getItem: key => entries.get(key) ?? null,
    key: index => [...entries.keys()][index] ?? null,
    removeItem: key => { entries.delete(key) },
    setItem: (key, value) => { entries.set(key, value) },
  }
}

function concierge(overrides: Partial<ConciergeItem> = {}): ConciergeItem {
  return {
    name: 'advisor',
    nickname: 'advisor',
    description: '',
    identity: 'default',
    reasoning_effort: 'high',
    impressions: [],
    tool_groups: ['basic', 'web'],
    default_tool_groups: ['basic', 'web'],
    plugins: [],
    default_plugins: [],
    ...overrides,
  }
}

describe('composerReasoningEffort', () => {
  it('keeps the composer choices and collapses everything else to none', () => {
    expect(composerReasoningEffort('max')).toBe('max')
    expect(composerReasoningEffort('high')).toBe('high')
    expect(composerReasoningEffort('low')).toBe('low')
    expect(composerReasoningEffort('none')).toBe('none')
    expect(composerReasoningEffort('')).toBe('none')
    expect(composerReasoningEffort(undefined)).toBe('none')
    expect(composerReasoningEffort('extreme')).toBe('none')
  })
})

describe('webSearchSelectable', () => {
  it('allows 联网 when the Concierge offers the web tool group', () => {
    expect(webSearchSelectable({ tool_groups: ['basic', 'web'] })).toBe(true)
    expect(webSearchSelectable({ tool_groups: ['basic', 'web'] }, [])).toBe(true)
  })

  it('allows 联网 for a session that already carries the web tool group', () => {
    expect(webSearchSelectable({ tool_groups: ['basic'] }, ['web'])).toBe(true)
    expect(webSearchSelectable(null, ['web'])).toBe(true)
  })

  it('rejects 联网 when nothing provides the web tool group', () => {
    expect(webSearchSelectable({ tool_groups: ['basic'] }, ['basic'])).toBe(false)
    expect(webSearchSelectable({ tool_groups: [] }, [])).toBe(false)
    expect(webSearchSelectable(null)).toBe(false)
    expect(webSearchSelectable(undefined, null)).toBe(false)
  })
})

describe('toggleAuthorizationMode', () => {
  it('flips between asking and allowing everything', () => {
    expect(toggleAuthorizationMode('askEachTime')).toBe('allowAll')
    expect(toggleAuthorizationMode('allowAll')).toBe('askEachTime')
    expect(toggleAuthorizationMode(toggleAuthorizationMode('askEachTime'))).toBe('askEachTime')
  })
})

describe('toggleReasoningEffort', () => {
  it('turns thinking on at the default level when it is off', () => {
    expect(toggleReasoningEffort('none')).toBe('high')
  })

  it('turns thinking off from every level, not to another level', () => {
    expect(toggleReasoningEffort('low')).toBe('none')
    expect(toggleReasoningEffort('high')).toBe('none')
    expect(toggleReasoningEffort('max')).toBe('none')
  })

  it('round-trips between off and the default level', () => {
    expect(toggleReasoningEffort(toggleReasoningEffort('none'))).toBe('none')
  })
})

describe('supportsWebSearch', () => {
  it('detects the web tool group and tolerates missing lists', () => {
    expect(supportsWebSearch(['basic', 'web'])).toBe(true)
    expect(supportsWebSearch(['basic'])).toBe(false)
    expect(supportsWebSearch([])).toBe(false)
    expect(supportsWebSearch(null)).toBe(false)
    expect(supportsWebSearch(undefined)).toBe(false)
  })
})

describe('web search preference cache', () => {
  it('keys the cache by concierge', () => {
    expect(webSearchStorageKey('advisor')).toBe('hephaestus.webSearch.advisor')
    expect(webSearchStorageKey('advisor')).not.toBe(webSearchStorageKey('assistant'))
  })

  it('round-trips each Concierge choice without leaking into the other', () => {
    const storage = fakeStorage()

    rememberWebSearchPreference('advisor', false, storage)
    rememberWebSearchPreference('assistant', true, storage)

    expect(readWebSearchPreference('advisor', storage)).toBe(false)
    expect(readWebSearchPreference('assistant', storage)).toBe(true)
  })

  it('reports unset and malformed entries as null', () => {
    const storage = fakeStorage({ [webSearchStorageKey('advisor')]: 'maybe' })

    expect(readWebSearchPreference('advisor', storage)).toBeNull()
    expect(readWebSearchPreference('assistant', storage)).toBeNull()
  })
})

describe('conciergeComposerOptions', () => {
  it('takes reasoning effort from the Identity and 联网 from web-capable defaults', () => {
    expect(conciergeComposerOptions(concierge(), fakeStorage())).toEqual({ reasoningEffort: 'high', webSearch: true })
  })

  it('defaults 联网 off when the Concierge cannot provide web tools', () => {
    const options = conciergeComposerOptions(concierge({ tool_groups: ['basic'], default_tool_groups: ['basic'] }), fakeStorage())

    expect(options).toEqual({ reasoningEffort: 'high', webSearch: false })
  })

  it('defaults 联网 off when the Concierge exposes web tools but does not enable them', () => {
    const options = conciergeComposerOptions(concierge({ default_tool_groups: ['basic'] }), fakeStorage())

    expect(options.webSearch).toBe(false)
  })

  it('prefers the 联网 choice remembered for that Concierge', () => {
    const storage = fakeStorage({ [webSearchStorageKey('advisor')]: 'false' })

    expect(conciergeComposerOptions(concierge(), storage).webSearch).toBe(false)
    expect(conciergeComposerOptions(concierge({ name: 'assistant' }), storage).webSearch).toBe(true)
  })
})

describe('sessionOptionOverrides', () => {
  it('leaves a session that already matches the Identity-derived defaults untouched', () => {
    expect(sessionOptionOverrides({ ReasoningEffort: 'high', EnableWebSearch: true }, { reasoningEffort: 'high', webSearch: true })).toBeNull()
  })

  it('patches only the controls the user changed', () => {
    expect(sessionOptionOverrides({ ReasoningEffort: 'high', EnableWebSearch: true }, { reasoningEffort: 'max', webSearch: true })).toEqual({ reasoningEffort: 'max' })
    expect(sessionOptionOverrides({ ReasoningEffort: 'high', EnableWebSearch: true }, { reasoningEffort: 'high', webSearch: false })).toEqual({ enableWebSearch: false })
    expect(sessionOptionOverrides({ ReasoningEffort: 'high', EnableWebSearch: true }, { reasoningEffort: 'none', webSearch: false })).toEqual({ reasoningEffort: 'none', enableWebSearch: false })
  })

  it('treats an unset session 联网 state as enabled and an unset effort as none', () => {
    expect(sessionOptionOverrides({ ReasoningEffort: '', EnableWebSearch: null }, { reasoningEffort: 'none', webSearch: true })).toBeNull()
    expect(sessionOptionOverrides({ ReasoningEffort: '', EnableWebSearch: null }, { reasoningEffort: 'low', webSearch: false })).toEqual({ reasoningEffort: 'low', enableWebSearch: false })
  })
})
