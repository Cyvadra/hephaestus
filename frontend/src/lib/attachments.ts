export interface ParsedAttachment {
  path: string
  size: string
  contentIncluded: boolean
}

export interface ParsedUserContent {
  attachments: ParsedAttachment[]
  body: string
}

export function parseAttachmentPrefix(content: string): ParsedUserContent | null {
  const attachments: ParsedAttachment[] = []
  let remaining = content
  while (remaining.startsWith('[file name]: ')) {
    const match = remaining.match(/^\[file name\]: ([^\n]+)\n\[file size\]: ([^\n]+)\n(?:\[file content begin\]\n[\s\S]*?\n\[file content end\]\n)?\n/)
    if (!match) return null
    attachments.push({ path: match[1], size: match[2], contentIncluded: match[0].includes('[file content begin]') })
    remaining = remaining.slice(match[0].length)
  }
  return attachments.length === 0 ? null : { attachments, body: remaining }
}

export function pendingAttachmentPrefix(files: File[]): string {
  return files.map(file => (
    `[file name]: uploads/pending/${file.name}\n[file size]: ${formatSize(file.size)}\n\n`
  )).join('')
}

function formatSize(size: number): string {
  return size >= 1024 * 1024 ? `${(size / (1024 * 1024)).toFixed(1)} MB` : `${(size / 1024).toFixed(1)} KB`
}