import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import { resources } from './locales'

export const LANGUAGE_STORAGE_KEY = 'hephaestus.language'
export const supportedLanguages = ['zh-CN', 'en-US'] as const

function resolveLanguage() {
  const storedLanguage = localStorage.getItem(LANGUAGE_STORAGE_KEY)
  if (storedLanguage != null && supportedLanguages.includes(storedLanguage as (typeof supportedLanguages)[number])) {
    return storedLanguage
  }
  return navigator.language.startsWith('zh') ? 'zh-CN' : 'en-US'
}

void i18n
  .use(initReactI18next)
  .init({
    resources,
    lng: resolveLanguage(),
    fallbackLng: 'zh-CN',
    interpolation: { escapeValue: false },
  })

i18n.on('languageChanged', language => localStorage.setItem(LANGUAGE_STORAGE_KEY, language))

export default i18n