import { Check, Globe2, Settings } from 'lucide-react'
import { useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { supportedLanguages } from '../i18n'
import { useHoverMenu } from '../lib/useHoverMenu'

interface Props {
  mode: 'chat' | 'configurations'
  onOpenConfigurations: () => void
  onCloseConfigurations: () => void
}

export default function SidebarSettingsMenu({ mode, onOpenConfigurations, onCloseConfigurations }: Props) {
  const { t, i18n } = useTranslation()
  const rootRef = useRef<HTMLDivElement>(null)
  const menu = useHoverMenu(rootRef)
  const configurationLabel = mode === 'configurations' ? t('app.returnToChat') : t('app.configurationManagement')

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
        <button className="sidebar-settings-option" type="button" role="menuitem" onClick={openConfiguration}>
          <Settings aria-hidden="true" size={15} />
          <span>{configurationLabel}</span>
        </button>
      </div>}
    </div>
  )
}