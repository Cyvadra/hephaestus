import { Check, Globe2, LogOut, Moon, Settings } from 'lucide-react'
import { useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { supportedLanguages } from '../i18n'
import { useHoverMenu } from '../lib/useHoverMenu'
import { logout } from '../api/auth'

const NIGHT_MODE_STORAGE_KEY = 'hephaestus.nightMode'

interface Props {
  mode: 'chat' | 'configurations'
  onOpenConfigurations: () => void
  onCloseConfigurations: () => void
}

export default function SidebarSettingsMenu({ mode, onOpenConfigurations, onCloseConfigurations }: Props) {
  const { t, i18n } = useTranslation()
  const rootRef = useRef<HTMLDivElement>(null)
  const menu = useHoverMenu(rootRef)
  const [nightMode, setNightMode] = useState(() => localStorage.getItem(NIGHT_MODE_STORAGE_KEY) === 'true')
  const configurationLabel = mode === 'configurations' ? t('app.returnToChat') : t('app.configurationManagement')

  useLayoutEffect(() => {
    document.body.toggleAttribute('data-ds-dark-theme', nightMode)
    document.body.classList.toggle('dark', nightMode)
    localStorage.setItem(NIGHT_MODE_STORAGE_KEY, String(nightMode))
  }, [nightMode])

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
        <button
          className="sidebar-settings-option"
          type="button"
          role="menuitemcheckbox"
          aria-checked={nightMode}
          onClick={() => setNightMode(enabled => !enabled)}
        >
          <span className="sidebar-settings-option-label"><Moon aria-hidden="true" size={15} />{t('app.nightMode')}</span>
          <span className="sidebar-settings-switch" aria-hidden="true"><span /></span>
        </button>
        <button className="sidebar-settings-option" type="button" role="menuitem" onClick={openConfiguration}>
          <Settings aria-hidden="true" size={15} />
          <span>{configurationLabel}</span>
        </button>
      </div>}
    </div>
  )
}