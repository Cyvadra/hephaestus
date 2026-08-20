export interface TerminalOutputState {
  text: string
  cursor: number
  pendingControl?: string
  carriageReturn?: boolean
}

const escapeCharacter = String.fromCharCode(27)
const ansiSequence = new RegExp(`^${escapeCharacter}\\[[0-?]*[ -/]*[@-~]`)

export function appendTerminalOutput(
  state: TerminalOutputState,
  chunk: string,
): TerminalOutputState {
  const output = [...state.text]
  let cursor = Math.min(state.cursor, output.length)
  let pendingControl = state.pendingControl ?? ''
  let carriageReturn = state.carriageReturn ?? false
  chunk = pendingControl + chunk
  pendingControl = ''

  for (let offset = 0; offset < chunk.length;) {
    const remaining = chunk.slice(offset)
    const ansi = remaining.match(ansiSequence)?.[0]
    if (ansi) {
      if (ansi.endsWith('K')) eraseLine(output, cursor, ansi.at(-2) ?? '0')
      offset += ansi.length
      continue
    }
  if (remaining === escapeCharacter || (remaining.startsWith(`${escapeCharacter}[`) && !/[@-~]/.test(remaining.slice(2)))) {
    pendingControl = remaining
    break
  }

    const character = chunk[offset]
    offset++
    if (character === '\r') {
      cursor = lineStart(output, cursor)
    carriageReturn = true
    } else if (character === '\n') {
    if (carriageReturn) {
    const end = lineEnd(output, cursor)
    cursor = output[end] === '\n' ? end + 1 : writeCharacter(output, end, '\n')
    } else {
    cursor = writeCharacter(output, cursor, '\n')
    }
    carriageReturn = false
    } else if (character === '\b') {
      cursor = Math.max(lineStart(output, cursor), cursor - 1)
    carriageReturn = false
    } else if (character >= ' ' || character === '\t') {
      cursor = writeCharacter(output, cursor, character)
    carriageReturn = false
    }
  }

  return { text: output.join(''), cursor, pendingControl, carriageReturn }
}

export function renderTerminalOutput(output: string): TerminalOutputState {
  return appendTerminalOutput({ text: '', cursor: 0 }, output)
}

function writeCharacter(output: string[], cursor: number, character: string): number {
  if (cursor === output.length) output.push(character)
  else output[cursor] = character
  return cursor + 1
}

function lineStart(output: string[], cursor: number): number {
  if (cursor === 0) return 0
  return output.lastIndexOf('\n', cursor - 1) + 1
}

function lineEnd(output: string[], cursor: number): number {
  const newline = output.indexOf('\n', cursor)
  return newline === -1 ? output.length : newline
}

function eraseLine(output: string[], cursor: number, mode: string): void {
  const start = lineStart(output, cursor)
  const newline = output.indexOf('\n', cursor)
  const end = newline === -1 ? output.length : newline
  if (mode === '1') output.splice(start, cursor - start, ...Array(cursor - start).fill(' '))
  else if (mode === '2') output.splice(start, end - start)
  else output.splice(cursor, end - cursor)
}