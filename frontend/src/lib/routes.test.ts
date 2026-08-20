import { describe, expect, it } from 'vitest'
import { parseRoute, routes } from './routes'

describe('routes', () => {
  it('round-trips encoded project names', () => {
    expect(parseRoute(routes.chatNew('project alpha/中文'))).toEqual({
      type: 'chat-new',
      project: 'project alpha/中文',
    })
    expect(routes.chat('default-workspace', 308)).toBe('/default-workspace/308')
  })

  it('round-trips encoded configuration names', () => {
    expect(parseRoute(routes.configurationEdit('identities', 'name with/slash'))).toEqual({
      type: 'configuration-edit',
      kind: 'identities',
      name: 'name with/slash',
    })
    expect(parseRoute('/configurations/constants')).toEqual({ type: 'configuration-constants' })
  })

  it('rejects invalid session identifiers', () => {
    expect(parseRoute('/default/0')).toEqual({ type: 'invalid' })
    expect(parseRoute('/default/not-a-number')).toEqual({ type: 'invalid' })
  })
})