import { describe, expect, it } from 'vitest'
import { parseAttachmentPrefix, pendingAttachmentPrefix } from './attachments'

describe('attachment prefixes', () => {
  it('round-trips pending file metadata', () => {
    const prefix = pendingAttachmentPrefix([
      { name: 'a.txt', size: 12 } as File,
      { name: 'b.json', size: 34 } as File,
    ])

    expect(parseAttachmentPrefix(`${prefix}hello`)).toEqual({
      attachments: [
        { path: 'uploads/pending/a.txt', size: '0.0 KB', contentIncluded: false },
        { path: 'uploads/pending/b.json', size: '0.0 KB', contentIncluded: false },
      ],
      body: 'hello',
    })
  })

  it('returns null for ordinary chat content', () => {
    expect(parseAttachmentPrefix('hello')).toBeNull()
  })
})