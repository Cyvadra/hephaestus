import { describe, expect, it } from 'vitest'
import { parseAskQuestionsHistory } from './askQuestionsHistory'

describe('parseAskQuestionsHistory', () => {
  const argumentsText = JSON.stringify({ questions: [{ id: 'stack', prompt: 'Choose a stack', multi_select: false, options: [{ id: 'go', title: 'Go', description: 'Compiled' }, { id: 'ts', title: 'TypeScript', description: 'Typed' }] }] })

  it('matches selected options and custom text to their question', () => {
    const history = parseAskQuestionsHistory(argumentsText, JSON.stringify({ answers: [{ question_id: 'stack', selected_option_ids: ['go'], custom_text: 'Also use SQL' }] }))

    expect(history?.questions[0].prompt).toBe('Choose a stack')
    expect(history?.answersByQuestionID.get('stack')).toEqual({ question_id: 'stack', selected_option_ids: ['go'], custom_text: 'Also use SQL' })
  })

  it('falls back when the stored data is malformed', () => {
    expect(parseAskQuestionsHistory(argumentsText, '{')).toBeNull()
    expect(parseAskQuestionsHistory(argumentsText, JSON.stringify({ answers: [] }))).toBeNull()
  })

  it('supports historical calls created before multi_select was included', () => {
    const legacyArguments = JSON.stringify({ questions: [{ id: 'stack', prompt: 'Choose a stack', options: [{ id: 'go', title: 'Go', description: 'Compiled' }, { id: 'ts', title: 'TypeScript', description: 'Typed' }] }] })
    const history = parseAskQuestionsHistory(legacyArguments, JSON.stringify({ answers: [{ question_id: 'stack', selected_option_ids: [], custom_text: 'Rebuild it' }] }))

    expect(history?.questions[0].multi_select).toBe(false)
    expect(history?.answersByQuestionID.get('stack')?.custom_text).toBe('Rebuild it')
  })

  it('renders the observed legacy custom-text-only history shape', () => {
    const legacyArguments = JSON.stringify({ questions: [{ id: 'quant_layer', prompt: 'Which layer?', options: [{ id: 'arch', title: 'Architecture', description: 'Design work' }, { id: 'ops', title: 'Operations', description: 'Deployment work' }] }] })
    const legacyResult = JSON.stringify({ answers: [{ question_id: 'quant_layer', selected_option_ids: [], custom_text: 'Refactoring the architecture' }] })
    const history = parseAskQuestionsHistory(legacyArguments, legacyResult)

    expect(history?.questions).toHaveLength(1)
    expect(history?.answersByQuestionID.get('quant_layer')).toMatchObject({ selected_option_ids: [], custom_text: 'Refactoring the architecture' })
  })
})