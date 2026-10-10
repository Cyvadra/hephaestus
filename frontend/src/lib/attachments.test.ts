import { describe, expect, it } from 'vitest'
import { filesFromClipboard, parseAttachmentPrefix, pendingAttachmentPrefix } from './attachments'

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

  it('parses extra metadata lines and excerpts', () => {
    const content = '[file name]: uploads/x.png\n[file size]: 1.0 KB\n[mime type]: image/png\n[image size]: 10x20\n\n'
      + '[file name]: uploads/y.md\n[file size]: 99.0 KB\n[file excerpt]: head/tail\n[file content begin]\nhead\n...\ntail\n[file content end]\n\nbody'
    expect(parseAttachmentPrefix(content)).toEqual({
      attachments: [
        { path: 'uploads/x.png', size: '1.0 KB', contentIncluded: false },
        { path: 'uploads/y.md', size: '99.0 KB', contentIncluded: true },
      ],
      body: 'body',
    })
  })

  it('names pasted images and skips text items', () => {
    const image = new File(['x'], 'image.png', { type: 'image/png' })
    const doc = new File(['y'], 'notes.txt', { type: 'text/plain' })
    const files = filesFromClipboard([
      { kind: 'string', type: 'text/plain', getAsFile: () => null },
      { kind: 'file', type: 'image/png', getAsFile: () => image },
      { kind: 'file', type: 'text/plain', getAsFile: () => doc },
    ], new Date(2026, 9, 10, 8, 5, 3))
    expect(files.map(f => f.name)).toEqual(['pasted-20261010-080503-1.png', 'notes.txt'])
    expect(files[0].type).toBe('image/png')
  })
})
