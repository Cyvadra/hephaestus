import CodeMirror from '@uiw/react-codemirror'
import { markdown } from '@codemirror/lang-markdown'
import { oneDark } from '@codemirror/theme-one-dark'
import { EditorView } from '@codemirror/view'
import { Bold, Code2, Eye, Italic, Maximize2, PanelsTopLeft, Type } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import Markdown from '../Markdown'

type Mode = 'edit' | 'split' | 'preview'

export default function MarkdownEditor({ value, onChange }: { value: string; onChange: (value: string) => void }) {
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
    const addition = `${before}文本${after}`
    onChange(value ? `${value}${value.endsWith('\n') ? '' : '\n'}${addition}` : addition)
  }

  return (
    <div ref={editorRef} className={`configuration-markdown-editor${fullscreen ? ' fullscreen' : ''}`}>
      <div className="configuration-editor-toolbar">
        <div className="configuration-editor-tools">
          <button type="button" title="加粗" onClick={() => wrap('**')}><Bold size={15} /></button>
          <button type="button" title="斜体" onClick={() => wrap('_')}><Italic size={15} /></button>
          <button type="button" title="标题" onClick={() => wrap('## ', '')}><Type size={15} /></button>
          <button type="button" title="行内代码" onClick={() => wrap('`')}><Code2 size={15} /></button>
        </div>
        <div className="configuration-editor-modes" role="group" aria-label="Markdown 显示模式">
          <button type="button" className={mode === 'edit' ? 'active' : ''} onClick={() => setMode('edit')}>编辑</button>
          <button type="button" className={mode === 'split' ? 'active' : ''} onClick={() => setMode('split')}><PanelsTopLeft size={14} />分屏</button>
          <button type="button" className={mode === 'preview' ? 'active' : ''} onClick={() => setMode('preview')}><Eye size={14} />预览</button>
        </div>
        <button className="configuration-editor-fullscreen" type="button" title={fullscreen ? '退出全屏' : '全屏'} onClick={() => setFullscreen(current => !current)}><Maximize2 size={15} /></button>
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
            {value ? <Markdown>{value}</Markdown> : <p className="configuration-preview-empty">Markdown 预览将显示在这里</p>}
          </div>
        )}
      </div>
    </div>
  )
}
