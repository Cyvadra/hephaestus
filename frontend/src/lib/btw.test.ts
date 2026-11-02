import { describe, expect, it } from 'vitest'
import { parseBtwCommand } from './btw'

describe('parseBtwCommand', () => {
  it('recognizes the command and returns the question', () => {
    expect(parseBtwCommand('/btw why?')).toBe('why?')
    expect(parseBtwCommand('/btw   why?')).toBe('why?')
    expect(parseBtwCommand('/btw\twhy?')).toBe('why?')
    expect(parseBtwCommand('  /btw why?  ')).toBe('why?')
  })

  it('returns an empty question for a bare /btw', () => {
    expect(parseBtwCommand('/btw')).toBe('')
    expect(parseBtwCommand('/btw   ')).toBe('')
    expect(parseBtwCommand('  /btw ')).toBe('')
  })

  it('ignores commands that only share the prefix', () => {
    expect(parseBtwCommand('/btwx')).toBeNull()
    expect(parseBtwCommand('/btwq why?')).toBeNull()
    expect(parseBtwCommand('/btw-why')).toBeNull()
  })

  it('ignores plain text and other commands', () => {
    expect(parseBtwCommand('why?')).toBeNull()
    expect(parseBtwCommand('mention /btw inline')).toBeNull()
    expect(parseBtwCommand('/status')).toBeNull()
    expect(parseBtwCommand('')).toBeNull()
  })
})
