import { describe, expect, it } from 'vitest'
import { parseKeywordQuery } from './searchQuery'

describe('parseKeywordQuery', () => {
  it('splits short unpunctuated queries into keywords', () => {
    expect(parseKeywordQuery('rocket engine')).toEqual(['rocket', 'engine'])
    expect(parseKeywordQuery(' a, b,c d ')).toEqual(['a', 'b', 'c', 'd'])
    expect(parseKeywordQuery('火箭 引擎')).toEqual(['火箭', '引擎'])
  })

  it('keeps phrase mode otherwise', () => {
    expect(parseKeywordQuery('rocket')).toBeNull()
    expect(parseKeywordQuery('a b c d e')).toBeNull()
    expect(parseKeywordQuery('rocket engine.')).toBeNull()
    expect(parseKeywordQuery('火箭，引擎')).toBeNull()
    expect(parseKeywordQuery(`${'x'.repeat(48)} y`)).toBeNull()
  })
})
