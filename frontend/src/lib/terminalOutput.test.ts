import { describe, expect, it } from 'vitest'
import { renderTerminalOutput } from './terminalOutput'

describe('renderTerminalOutput', () => {
  it('applies carriage-return progress updates', () => {
    expect(renderTerminalOutput('Progress 10%\rProgress 90%').text).toBe('Progress 90%')
  })

  it('applies backspace edits', () => {
    expect(renderTerminalOutput('abc\b\bd').text).toBe('adc')
  })

  it('removes ANSI control sequences', () => {
    expect(renderTerminalOutput('\u001b[31merror\u001b[0m').text).toBe('error')
  })
})