import { Check, Globe2, LogOut, Monitor, Moon, Settings, Sun } from 'lucide-react'
import { useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { supportedLanguages } from '../i18n'
import { useHoverMenu } from '../lib/useHoverMenu'
import { logout } from '../api/auth'

const NIGHT_MODE_STORAGE_KEY = 'hephaestus.nightMode'
type ThemeMode = 'light' | 'dark' | 'system'

function getThemeMode(): ThemeMode {
  const storedMode = localStorage.getItem(NIGHT_MODE_STORAGE_KEY)
  if (storedMode === 'true' || storedMode === 'dark') return 'dark'
  if (storedMode === 'false' || storedMode === 'light') return 'light'
  return 'system'
}

interface Props {
  mode: 'chat' | 'configurations'
  onOpenConfigurations: () => void
  onCloseConfigurations: () => void
}

export default function SidebarSettingsMenu({ mode, onOpenConfigurations, onCloseConfigurations }: Props) {
  const { t, i18n } = useTranslation()
  const rootRef = useRef<HTMLDivElement>(null)
  const menu = useHoverMenu(rootRef)
  const [themeMode, setThemeMode] = useState<ThemeMode>(getThemeMode)
  const [systemPrefersDark, setSystemPrefersDark] = useState(() => window.matchMedia('(prefers-color-scheme: dark)').matches)
  const configurationLabel = mode === 'configurations' ? t('app.returnToChat') : t('app.configurationManagement')
  const darkTheme = themeMode === 'dark' || (themeMode === 'system' && systemPrefersDark)

  useLayoutEffect(() => {
    document.body.toggleAttribute('data-ds-dark-theme', darkTheme)
    document.body.classList.toggle('dark', darkTheme)
    localStorage.setItem(NIGHT_MODE_STORAGE_KEY, themeMode)
  }, [darkTheme, themeMode])

  useLayoutEffect(() => {
    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
    const updateSystemPreference = (event: MediaQueryListEvent) => setSystemPrefersDark(event.matches)
    mediaQuery.addEventListener('change', updateSystemPreference)
    return () => mediaQuery.removeEventListener('change', updateSystemPreference)
  }, [])

  const openConfiguration = () => {
    if (mode === 'configurations') onCloseConfigurations()
    else onOpenConfigurations()
    menu.close()
  }

  return (
    <div className="sidebar-settings-menu" ref={rootRef} onMouseEnter={menu.openOnHover} onMouseLeave={menu.scheduleClose}>
      <button
        className={`sidebar-settings-btn${mode === 'configurations' ? ' active' : ''}`}
        type="button"
        aria-label={t('app.settings')}
        aria-expanded={menu.open}
        aria-haspopup="menu"
        onClick={openConfiguration}
        onFocus={menu.pinOpen}
      >
        <Settings aria-hidden="true" size={16} strokeWidth={1.7} />
      </button>
      {menu.open && <div className="sidebar-settings-popover" role="menu" aria-label={t('app.settings')}>
        {/* Keep the least-used action farthest from the user's most recent settings interaction. */}
        <button className="sidebar-settings-option" type="button" role="menuitem" onClick={() => { void logout(); menu.close() }}>
          <LogOut aria-hidden="true" size={15} />
          <span>{t('auth.logout')}</span>
        </button>
        <div className="sidebar-settings-divider" />
        <div className="sidebar-settings-heading"><Globe2 aria-hidden="true" size={14} />{t('app.language')}</div>
        {supportedLanguages.map(language => <button
          className="sidebar-settings-option"
          type="button"
          role="menuitemradio"
          key={language}
          aria-checked={i18n.resolvedLanguage === language}
          onClick={() => { void i18n.changeLanguage(language); menu.close() }}
        >
          <span>{t(`app.languages.${language}`)}</span>
          {i18n.resolvedLanguage === language && <Check aria-label={t('app.selectedLanguage', { language: t(`app.languages.${language}`) })} size={15} />}
        </button>)}
        <div className="sidebar-settings-divider" />
        <div className="sidebar-settings-heading">{t('app.theme')}</div>
        <div className="sidebar-theme-selector" role="radiogroup" aria-label={t('app.theme')}>
          <button className="sidebar-theme-option" type="button" role="radio" aria-checked={themeMode === 'light'} onClick={() => setThemeMode('light')} title={t('app.themeLight')}><Sun aria-hidden="true" size={14} /><span>{t('app.themeLight')}</span></button>
          <button className="sidebar-theme-option" type="button" role="radio" aria-checked={themeMode === 'dark'} onClick={() => setThemeMode('dark')} title={t('app.themeDark')}><Moon aria-hidden="true" size={14} /><span>{t('app.themeDark')}</span></button>
          <button className="sidebar-theme-option" type="button" role="radio" aria-checked={themeMode === 'system'} onClick={() => setThemeMode('system')} title={t('app.themeSystem')}><Monitor aria-hidden="true" size={14} /><span>{t('app.themeSystem')}</span></button>
        </div>
        <button className="sidebar-settings-option" type="button" role="menuitem" onClick={openConfiguration}>
          <Settings aria-hidden="true" size={15} />
          <span>{configurationLabel}</span>
        </button>
      </div>}
    </div>
  )
}