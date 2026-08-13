import CodeMirror from '@uiw/react-codemirror'
import { markdown } from '@codemirror/lang-markdown'
import { oneDark } from '@codemirror/theme-one-dark'
import { EditorView } from '@codemirror/view'
import { Bold, Code2, Eye, Italic, Maximize2, PanelsTopLeft, Type } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import Markdown from '../Markdown'

type Mode = 'edit' | 'split' | 'preview'

export default function MarkdownEditor({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const { t } = useTranslation()
  const [mode, setMode] = useState<Mode>('split')
  const [fullscreen, setFullscreen] = useState(false)
  const editorRef = useRef<HTMLDivElement>(null)
  const isDark = document.body.classList.contains('dark') || window.matchMedia('(prefers-color-scheme: dark)').matches

  useEffect(() => {
    if (mode !== 'split') return

    let syncing = false
    let editorScroller: HTMLElement | null = null
    let previewScroller: HTMLElement | null = null
    let syncPreview: (() => void) | null = null
    let syncEditor: (() => void) | null = null
    const frame = requestAnimationFrame(() => {
      const root = editorRef.current
      editorScroller = root?.querySelector<HTMLElement>('.cm-scroller') ?? null
      previewScroller = root?.querySelector<HTMLElement>('.configuration-markdown-preview') ?? null
      if (!editorScroller || !previewScroller) return

      const syncScroll = (source: HTMLElement, target: HTMLElement) => {
        if (syncing) return
        syncing = true
        const sourceRange = source.scrollHeight - source.clientHeight
        const targetRange = target.scrollHeight - target.clientHeight
        target.scrollTop = sourceRange > 0 ? source.scrollTop / sourceRange * targetRange : 0
        syncing = false
      }
      syncPreview = () => syncScroll(editorScroller!, previewScroller!)
      syncEditor = () => syncScroll(previewScroller!, editorScroller!)
      editorScroller.addEventListener('scroll', syncPreview)
      previewScroller.addEventListener('scroll', syncEditor)
    })

    return () => {
      cancelAnimationFrame(frame)
      if (editorScroller && syncPreview) editorScroller.removeEventListener('scroll', syncPreview)
      if (previewScroller && syncEditor) previewScroller.removeEventListener('scroll', syncEditor)
    }
  }, [mode, fullscreen])

  const wrap = (before: string, after = before) => {
    const addition = `${before}${t('configuration.markdown.sampleText')}${after}`
    onChange(value ? `${value}${value.endsWith('\n') ? '' : '\n'}${addition}` : addition)
  }

  return (
    <div ref={editorRef} className={`configuration-markdown-editor${fullscreen ? ' fullscreen' : ''}`}>
      <div className="configuration-editor-toolbar">
        <div className="configuration-editor-tools">
          <button type="button" title={t('configuration.markdown.bold')} onClick={() => wrap('**')}><Bold size={15} /></button>
          <button type="button" title={t('configuration.markdown.italic')} onClick={() => wrap('_')}><Italic size={15} /></button>
          <button type="button" title={t('configuration.markdown.heading')} onClick={() => wrap('## ', '')}><Type size={15} /></button>
          <button type="button" title={t('configuration.markdown.inlineCode')} onClick={() => wrap('`')}><Code2 size={15} /></button>
        </div>
        <div className="configuration-editor-modes" role="group" aria-label={t('configuration.markdown.displayMode')}>
          <button type="button" className={mode === 'edit' ? 'active' : ''} onClick={() => setMode('edit')}>{t('configuration.markdown.edit')}</button>
          <button type="button" className={mode === 'split' ? 'active' : ''} onClick={() => setMode('split')}><PanelsTopLeft size={14} />{t('configuration.markdown.split')}</button>
          <button type="button" className={mode === 'preview' ? 'active' : ''} onClick={() => setMode('preview')}><Eye size={14} />{t('configuration.markdown.preview')}</button>
        </div>
        <button className="configuration-editor-fullscreen" type="button" title={fullscreen ? t('configuration.markdown.exitFullscreen') : t('configuration.markdown.fullscreen')} onClick={() => setFullscreen(current => !current)}><Maximize2 size={15} /></button>
      </div>
      <div className={`configuration-editor-body mode-${mode}`}>
        {mode !== 'preview' && (
          <CodeMirror
            value={value}
            height="100%"
            theme={isDark ? oneDark : 'light'}
            extensions={[markdown(), EditorView.lineWrapping]}
            onChange={onChange}
            basicSetup={{ lineNumbers: true, foldGutter: true, highlightActiveLine: true }}
          />
        )}
        {mode !== 'edit' && (
          <div className="configuration-markdown-preview ds-markdown">
            {value ? <Markdown>{value}</Markdown> : <p className="configuration-preview-empty">{t('configuration.markdown.emptyPreview')}</p>}
          </div>
        )}
      </div>
    </div>
  )
}
