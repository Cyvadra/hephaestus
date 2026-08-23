import { describe, expect, it } from 'vitest'
import { normalizeQuestionAnswers } from './questionAnswers'

const questions = [{ id: 'stack', prompt: 'Choose', multi_select: false, options: [{ id: 'go', title: 'Go', description: '' }, { id: 'ts', title: 'TypeScript', description: '' }] }]

describe('normalizeQuestionAnswers', () => {
  it('allows a preset option and custom text together', () => {
    expect(normalizeQuestionAnswers(questions, { stack: { selectedOptionIds: ['go'], customText: 'Also SQL' } })).toEqual([
      { question_id: 'stack', selected_option_ids: ['go'], custom_text: 'Also SQL' },
    ])
  })

  it('rejects an unanswered question', () => {
    expect(normalizeQuestionAnswers(questions, {})).toBeNull()
  })
})