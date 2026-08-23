import type { Question, QuestionAnswer } from '../api/types'

export interface AskQuestionsHistory {
  questions: Question[]
  answersByQuestionID: Map<string, QuestionAnswer>
}

// parseAskQuestionsHistory only enables the specialized display for complete,
// structurally valid data. Older or malformed tool records keep the generic
// JSON fallback instead of hiding diagnostic information.
export function parseAskQuestionsHistory(argumentsText?: string, resultText?: string): AskQuestionsHistory | null {
  if (!argumentsText || !resultText) return null
  try {
    const argumentsValue: unknown = JSON.parse(argumentsText)
    const resultValue: unknown = JSON.parse(resultText)
    if (!isRecord(argumentsValue) || !Array.isArray(argumentsValue.questions) || !isRecord(resultValue) || !Array.isArray(resultValue.answers)) return null

    const questions = argumentsValue.questions.map(normalizeQuestion).filter((question): question is Question => question !== null)
    const answers = resultValue.answers.filter(isAnswer)
    if (questions.length !== argumentsValue.questions.length || answers.length !== resultValue.answers.length) return null

    const answersByQuestionID = new Map(answers.map(answer => [answer.question_id, answer]))
    if (questions.some(question => !answersByQuestionID.has(question.id))) return null
    return { questions, answersByQuestionID }
  } catch {
    return null
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function normalizeQuestion(value: unknown): Question | null {
  if (!isRecord(value) || typeof value.id !== 'string' || typeof value.prompt !== 'string' || (value.multi_select !== undefined && typeof value.multi_select !== 'boolean') || !Array.isArray(value.options) || !value.options.every(option =>
    isRecord(option) && typeof option.id === 'string' && typeof option.title === 'string' && typeof option.description === 'string',
  )) return null
  return { id: value.id, prompt: value.prompt, multi_select: value.multi_select ?? false, options: value.options as Question['options'] }
}

function isAnswer(value: unknown): value is QuestionAnswer {
  return isRecord(value) && typeof value.question_id === 'string' && Array.isArray(value.selected_option_ids) && value.selected_option_ids.every(optionID => typeof optionID === 'string') && (value.custom_text === undefined || typeof value.custom_text === 'string')
}