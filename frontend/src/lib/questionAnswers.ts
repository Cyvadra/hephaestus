import type { Question, QuestionAnswer } from '../api/types'

export type DraftQuestionAnswer = { selectedOptionIds: string[], customText: string }

export function normalizeQuestionAnswers(questions: Question[], drafts: Record<string, DraftQuestionAnswer>): QuestionAnswer[] | null {
  const answers: QuestionAnswer[] = []
  for (const question of questions) {
    const draft = drafts[question.id] ?? { selectedOptionIds: [], customText: '' }
    const selected = [...new Set(draft.selectedOptionIds)].filter(id => question.options.some(option => option.id === id))
    const customText = draft.customText.trim()
    if ((!question.multi_select && selected.length > 1) || (selected.length === 0 && customText === '')) return null
    answers.push({ question_id: question.id, selected_option_ids: selected, ...(customText ? { custom_text: customText } : {}) })
  }
  return answers
}