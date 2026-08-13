import { useEffect, useState, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { ArrowUp, Blocks, Check, Wrench, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { GenerationOptions, ReasoningEffort } from '../api/types'
import { useHoverMenu } from '../lib/useHoverMenu'

interface Props {
  onSend: (text: string, files: File[]) => void
  commandHelp: string | null
  commandHelpLoading: boolean
  onCommandHelpRequest: () => void
  onStop: () => void
  disabled: boolean
  files: File[]
  onFilesChange: (files: File[]) => void
  generationOptions: GenerationOptions
  onGenerationOptionsChange: (options: GenerationOptions) => void
  toolGroups: string[]
  activeToolGroups: string[]
  onToolGroupToggle: (toolGroup: string, active: boolean) => void
  plugins: string[]
  pluginDescriptions: Record<string, string>
  activePlugins: string[]
  onPluginToggle: (plugin: string, active: boolean) => void
}

const reasoningChoices: ReasoningEffort[] = [
  'max',
  'high',
  'none',
]

export default function Composer({ onSend, commandHelp, commandHelpLoading, onCommandHelpRequest, onStop, disabled, files, onFilesChange, generationOptions, onGenerationOptionsChange, toolGroups, activeToolGroups, onToolGroupToggle, plugins = [], pluginDescriptions = {}, activePlugins = [], onPluginToggle }: Props) {
  const { t } = useTranslation()
  const [text, setText] = useState('')
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const firstMatchedCommandRef = useRef<HTMLButtonElement>(null)
  const reasoningRef = useRef<HTMLDivElement>(null)
  const reasoningMenu = useHoverMenu(reasoningRef)

  const isCommand = text.trimStart().startsWith('/')
  const controlsDisabled = disabled || isCommand
  const reasoningLabel = reasoningChoices.includes(generationOptions.reasoningEffort)
    ? t(`chat.reasoning.${generationOptions.reasoningEffort}`)
    : t('chat.reasoning.unavailable')
  const commandQuery = text.trimStart().toLowerCase()
  const commandSuggestions = commandHelp
    ?.split('\n')
    .filter(line => line.trimStart().startsWith('/'))
    .sort((left, right) => left.localeCompare(right, 'en', { sensitivity: 'base' })) ?? []
  const matchedCommands = commandQuery === '/'
    ? []
    : commandSuggestions.filter(command => command.toLowerCase().startsWith(commandQuery))

  useEffect(() => {
    firstMatchedCommandRef.current?.scrollIntoView({ block: 'nearest' })
  }, [commandQuery, commandHelp])

  const handleTextChange = (value: string) => {
    setText(value)
    if (value.trimStart().startsWith('/')) onCommandHelpRequest()
  }

  const submit = () => {
    const t = text.trim()
    if (!t || disabled) return
    onSend(t, files)
    setText('')
    onFilesChange([])
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  const handleKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    const isModifiedSubmit = e.ctrlKey || e.metaKey
    const isCommandSubmit = isCommand && !e.shiftKey && !e.altKey
    if (e.key === 'Enter' && !e.nativeEvent.isComposing && (isModifiedSubmit || isCommandSubmit)) {
      e.preventDefault()
      submit()
    }
  }

  return (
    <div className="composer-panel">
      <div className="composer-card">
        {isCommand && (
          <div className="composer-hint">{t('chat.command.hint')}</div>
        )}
        {files.length > 0 && (
          <div className="composer-files" aria-live="polite">
            {files.map((file, index) => (
              <div className="composer-file" key={`${file.name}-${file.lastModified}-${index}`}>
                <span>{file.name} ({formatSize(file.size)})</span>
                <button type="button" onClick={() => onFilesChange(files.filter((_, currentIndex) => currentIndex !== index))} title={t('chat.files.remove', { name: file.name })} aria-label={t('chat.files.remove', { name: file.name })}>
                  <X aria-hidden="true" size={14} />
                </button>
              </div>
            ))}
          </div>
        )}
        {isCommand && (
          <div className="command-suggestions" role="listbox" aria-label={t('chat.command.suggestions')}>
            {commandHelpLoading ? <span className="command-suggestions-status">{t('chat.command.loading')}</span> : commandSuggestions.length > 0 ? (
              commandSuggestions.map(command => {
                const [name, ...description] = command.split(' - ')
                const isMatch = matchedCommands.includes(command)
                const isFirstMatch = command === matchedCommands[0]
                return (
                  <button
                    type="button"
                    role="option"
                    aria-selected={isMatch}
                    className={isMatch ? 'matched' : undefined}
                    ref={isFirstMatch ? firstMatchedCommandRef : undefined}
                    key={command}
                    onClick={() => {
                      setText(name)
                      textareaRef.current?.focus()
                    }}
                  >
                    <code>{name}</code>
                    {description.length > 0 && <span>{description.join(' - ')}</span>}
                  </button>
                )
              })
            ) : commandHelp ? <span className="command-suggestions-status">{t('chat.command.unavailable')}</span> : <span className="command-suggestions-status">{t('chat.command.pendingSession')}</span>}
          </div>
        )}
        <div className="composer-input-row">
          <textarea
            ref={textareaRef}
            value={text}
            onChange={e => handleTextChange(e.target.value)}
            onKeyDown={handleKey}
            disabled={disabled && !text.startsWith('/')}
            placeholder={disabled ? t('chat.compose.generating') : t('chat.compose.placeholder')}
            rows={3}
            className="composer-textarea"
          />
          <div className="composer-action-row">
            <div className="composer-generation-controls">
              <div
                className="composer-reasoning-control"
                ref={reasoningRef}
                onMouseEnter={() => { if (!controlsDisabled) reasoningMenu.openOnHover() }}
                onMouseLeave={reasoningMenu.scheduleClose}
              >
                <button
                  type="button"
                  className={'composer-option-btn' + (generationOptions.reasoningEffort !== 'none' ? ' active' : '')}
                  disabled={controlsDisabled}
                  aria-haspopup="menu"
                  aria-expanded={reasoningMenu.open}
                  onClick={reasoningMenu.pinOpen}
                  onFocus={() => {
                    if (!controlsDisabled) reasoningMenu.pinOpen()
                  }}
                  title={t('chat.reasoning.select')}
                >
                  <ThinkingIcon />
                  <span>{reasoningLabel}</span>
                </button>
                {reasoningMenu.open && (
                  <div className="composer-options-menu" role="menu" aria-label={t('chat.reasoning.select')} onMouseEnter={reasoningMenu.cancelClose} onMouseLeave={reasoningMenu.scheduleClose}>
                    {reasoningChoices.map(choice => (
                      <button
                        type="button"
                        role="menuitemradio"
                        aria-checked={generationOptions.reasoningEffort === choice}
                        key={choice}
                        onClick={() => {
                          onGenerationOptionsChange({ ...generationOptions, reasoningEffort: choice })
                          reasoningMenu.close()
                        }}
                      >
                        <span>{t(`chat.reasoning.${choice}`)}</span>
                        {generationOptions.reasoningEffort === choice && <Check aria-hidden="true" size={14} />}
                      </button>
                    ))}
                  </div>
                )}
              </div>
              <button
                type="button"
                className={'composer-option-btn' + (generationOptions.webSearch ? ' active' : '')}
                disabled={controlsDisabled}
                aria-pressed={generationOptions.webSearch}
                onClick={() => onGenerationOptionsChange({ ...generationOptions, webSearch: !generationOptions.webSearch })}
                title={generationOptions.webSearch ? t('chat.compose.webSearchEnabled') : t('chat.compose.webSearchDisabled')}
              >
                <WebIcon />
                <span>{t('chat.compose.webSearch')}</span>
              </button>
              {toolGroups.length > 0 && (
                <SelectableOptionsControl
                  label={t('chat.compose.tools')}
                  menuLabel={t('chat.compose.toolGroups')}
                  title={t('chat.compose.selectToolGroups')}
                  icon={<Wrench aria-hidden="true" size={14} />}
                  options={toolGroups}
                  activeOptions={activeToolGroups}
                  disabled={controlsDisabled}
                  onToggle={onToolGroupToggle}
                />
              )}
              {plugins.length > 0 && (
                <SelectableOptionsControl
                  label={t('chat.compose.plugins')}
                  menuLabel={t('chat.compose.plugins')}
                  title={t('chat.compose.selectPlugins')}
                  icon={<Blocks aria-hidden="true" size={14} />}
                  options={plugins}
                  activeOptions={activePlugins}
                  descriptions={pluginDescriptions}
                  disabled={controlsDisabled}
                  onToggle={onPluginToggle}
                />
              )}
            </div>
            <div className="composer-submit-controls">
              {disabled ? (
                <button type="button" onClick={onStop} className="composer-stop-btn">
                  {t('chat.compose.stop')}
                </button>
              ) : (
                <>
                <input ref={fileInputRef} type="file" multiple hidden onChange={event => onFilesChange([...files, ...Array.from(event.target.files ?? [])])} />
                <div className="composer-upload-tooltip">
                  <button type="button" onClick={() => fileInputRef.current?.click()} disabled={isCommand} className="composer-upload-btn" aria-label={t('chat.files.uploadLabel')} aria-describedby="upload-file-limits">
                    <svg aria-hidden="true" width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
                      <path d="M5.5498 9.75V5H6.9502V9.75C6.9502 10.3299 7.4201 10.7998 8 10.7998C8.5799 10.7998 9.0498 10.3299 9.0498 9.75V4.5C9.0498 2.9536 7.7964 1.7002 6.25 1.7002C4.7036 1.7002 3.4502 2.9536 3.4502 4.5V9.75C3.4502 12.2629 5.4871 14.2998 8 14.2998C10.5129 14.2998 12.5498 12.2629 12.5498 9.75V4H13.9502V9.75C13.9502 13.0361 11.2861 15.7002 8 15.7002C4.71391 15.7002 2.0498 13.0361 2.0498 9.75V4.5C2.04981 2.1804 3.9304 0.299806 6.25 0.299805C8.5696 0.299805 10.4502 2.1804 10.4502 4.5V9.75C10.4502 11.1031 9.3531 12.2002 8 12.2002C6.6469 12.2002 5.5498 11.1031 5.5498 9.75Z" fill="currentColor" />
                    </svg>
                  </button>
                  <span id="upload-file-limits" role="tooltip">{t('chat.files.uploadLimits')}</span>
                </div>
                <button
                  type="button"
                  onClick={submit}
                  disabled={!text.trim()}
                  className="composer-send-btn composer-send-icon-btn"
                  aria-label={t('chat.compose.send')}
                  title={t('chat.compose.send')}
                >
                  <ArrowUp aria-hidden="true" size={18} strokeWidth={2.5} />
                </button>
                </>
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

interface SelectableOptionsControlProps {
  label: string
  menuLabel: string
  title: string
  icon: ReactNode
  options: string[]
  activeOptions: string[]
  descriptions?: Record<string, string>
  disabled: boolean
  onToggle: (option: string, active: boolean) => void
}

function SelectableOptionsControl({ label, menuLabel, title, icon, options, activeOptions, descriptions = {}, disabled, onToggle }: SelectableOptionsControlProps) {
  const controlRef = useRef<HTMLDivElement>(null)
  const menu = useHoverMenu(controlRef)

  return (
    <div
      className="composer-reasoning-control"
      ref={controlRef}
      onMouseEnter={() => { if (!disabled) menu.openOnHover() }}
      onMouseLeave={menu.scheduleClose}
    >
      <button
        type="button"
        className={'composer-option-btn' + (menu.open ? ' active' : '')}
        disabled={disabled}
        aria-haspopup="menu"
        aria-expanded={menu.open}
        onClick={menu.pinOpen}
        onFocus={() => {
          if (!disabled) menu.pinOpen()
        }}
        title={title}
      >
        {icon}
        <span>{label}</span>
      </button>
      {menu.open && (
        <div className="composer-options-menu composer-selection-menu" role="menu" aria-label={menuLabel} onMouseEnter={menu.cancelClose} onMouseLeave={menu.scheduleClose}>
          {options.map(option => {
            const active = activeOptions.includes(option)
            return (
              <button
                type="button"
                role="menuitemcheckbox"
                aria-checked={active}
                key={option}
                title={descriptions[option]}
                onClick={() => onToggle(option, !active)}
              >
                <span>{option}</span>
                {active && <Check aria-hidden="true" size={14} />}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

function formatSize(size: number) {
  return size >= 1024 * 1024 ? `${(size / (1024 * 1024)).toFixed(1)} MB` : `${(size / 1024).toFixed(1)} KB`
}

// DeepSeek 官方思考图标（轨道圆环 + 中心点）
function ThinkingIcon() {
  return (
    <svg aria-hidden="true" width="14" height="14" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
      <path d="M8 6.77C8.6788 6.77 9.23 7.3212 9.23 8C9.23 8.6788 8.6788 9.23 8 9.23C7.3212 9.23 6.77 8.6788 6.77 8C6.77 7.3212 7.3212 6.77 8 6.77Z" fill="currentColor" />
      <path d="M10.5066 10.5066C7.3016 13.7116 3.5821 15.1861 2.198 13.802C0.8139 12.4179 2.2894 8.6984 5.4944 5.4934C8.6994 2.2884 12.4179 0.8139 13.802 2.198C15.1861 3.5821 13.7116 7.3016 10.5066 10.5066Z" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
      <path d="M10.731 5.269C13.936 8.474 15.31 12.294 13.802 13.802C12.294 15.31 8.475 13.936 5.27 10.731C2.065 7.526 0.69 3.706 2.198 2.198C3.706 0.69 7.526 2.064 10.731 5.269Z" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

// DeepSeek 官方联网图标（地球）
function WebIcon() {
  return (
    <svg aria-hidden="true" width="14" height="14" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
      <path d="M8 14.8492C9.5983 14.8492 10.8941 11.7828 10.8941 8C10.8941 4.2172 9.5983 1.1509 8 1.1509" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
      <path d="M8 14.8492C6.4009 14.8492 5.105 11.7828 5.105 8C5.105 4.2172 6.4009 1.1509 8 1.1509" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
      <path d="M8 1.1509C11.7824 1.1509 14.8487 4.2172 14.8487 8C14.8487 11.7828 11.7824 14.8492 8 14.8492C4.2168 14.8492 1.1504 11.7828 1.1504 8C1.1504 4.2172 4.2168 1.1509 8 1.1509Z" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
      <path d="M1.64 8C1.64 8 14.36 8 14.36 8" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}
