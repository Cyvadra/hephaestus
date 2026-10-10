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
    const match = remaining.match(/^\[file name\]: ([^\n]+)\n\[file size\]: ([^\n]+)\n(?:\[(?!file content begin\])[a-z ]+\]: [^\n]*\n)*(?:\[file content begin\]\n[\s\S]*?\n\[file content end\]\n)?\n/)
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

export function formatSize(size: number): string {
  return size >= 1024 * 1024 ? `${(size / (1024 * 1024)).toFixed(1)} MB` : `${(size / 1024).toFixed(1)} KB`
}
/** Extracts files from a clipboard paste, naming unnamed pasted images. */
export function filesFromClipboard(items: ArrayLike<{ kind: string; type: string; getAsFile(): File | null }>, now = new Date()): File[] {
  const stamp = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map(n => String(n).padStart(2, '0')).join('')
    + '-' + [now.getHours(), now.getMinutes(), now.getSeconds()].map(n => String(n).padStart(2, '0')).join('')
  const files: File[] = []
  for (const item of Array.from(items)) {
    if (item.kind !== 'file') continue
    const file = item.getAsFile()
    if (!file) continue
    const generic = !file.name || /^image\.\w+$/i.test(file.name)
    if (generic && file.type.startsWith('image/')) {
      const ext = file.type.slice('image/'.length).replace('jpeg', 'jpg').replace(/\+.*$/, '')
      files.push(new File([file], `pasted-${stamp}-${files.length + 1}.${ext}`, { type: file.type, lastModified: file.lastModified }))
    } else {
      files.push(file)
    }
  }
  return files
}
