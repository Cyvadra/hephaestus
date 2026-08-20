import { describe, expect, it } from 'vitest'
import { parseRoute, routes } from './routes'

describe('routes', () => {
  it('round-trips encoded project names', () => {
    expect(parseRoute(routes.chatNew('project alpha/中文'))).toEqual({
      type: 'chat-new',
      project: 'project alpha/中文',
    })
  })

  it('round-trips encoded configuration names', () => {
    expect(parseRoute(routes.configurationEdit('identities', 'name with/slash'))).toEqual({
      type: 'configuration-edit',
      kind: 'identities',
      name: 'name with/slash',
    })
  })

  it('rejects invalid session identifiers', () => {
    expect(parseRoute('/projects/default/chats/0')).toEqual({ type: 'invalid' })
    expect(parseRoute('/projects/default/chats/not-a-number')).toEqual({ type: 'invalid' })
  })
})