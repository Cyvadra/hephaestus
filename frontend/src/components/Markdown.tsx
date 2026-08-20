import { useEffect, useState, type ComponentPropsWithoutRef } from 'react'
import { Check, Copy } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import ReactMarkdown from 'react-markdown'
import { PrismLight as SyntaxHighlighter } from 'react-syntax-highlighter'
import bash from 'react-syntax-highlighter/dist/esm/languages/prism/bash'
import css from 'react-syntax-highlighter/dist/esm/languages/prism/css'
import go from 'react-syntax-highlighter/dist/esm/languages/prism/go'
import javascript from 'react-syntax-highlighter/dist/esm/languages/prism/javascript'
import json from 'react-syntax-highlighter/dist/esm/languages/prism/json'
import jsx from 'react-syntax-highlighter/dist/esm/languages/prism/jsx'
import markdown from 'react-syntax-highlighter/dist/esm/languages/prism/markdown'
import markup from 'react-syntax-highlighter/dist/esm/languages/prism/markup'
import python from 'react-syntax-highlighter/dist/esm/languages/prism/python'
import sql from 'react-syntax-highlighter/dist/esm/languages/prism/sql'
import typescript from 'react-syntax-highlighter/dist/esm/languages/prism/typescript'
import tsx from 'react-syntax-highlighter/dist/esm/languages/prism/tsx'
import yaml from 'react-syntax-highlighter/dist/esm/languages/prism/yaml'
import { oneDark, oneLight } from 'react-syntax-highlighter/dist/esm/styles/prism'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import { normalizeLatexDelimiters } from '../lib/markdown'

interface Props {
  children: string
}

type CodeProps = ComponentPropsWithoutRef<'code'>

Object.entries({ bash, shell: bash, css, go, javascript, js: javascript, json, jsx, markdown, markup, html: markup, python, py: python, sql, typescript, ts: typescript, tsx, yaml, yml: yaml })
  .forEach(([name, language]) => SyntaxHighlighter.registerLanguage(name, language))

function useDarkTheme() {
  const [isDarkTheme, setIsDarkTheme] = useState(() => document.body.hasAttribute('data-ds-dark-theme'))

  useEffect(() => {
    const observer = new MutationObserver(() => setIsDarkTheme(document.body.hasAttribute('data-ds-dark-theme')))
    observer.observe(document.body, { attributes: true, attributeFilter: ['data-ds-dark-theme'] })
    return () => observer.disconnect()
  }, [])

  return isDarkTheme
}

function CodeBlock({ className, children }: CodeProps) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)
  const isDarkTheme = useDarkTheme()
  const language = /language-([^\s]+)/.exec(className ?? '')?.[1] ?? 'text'
  const source = String(children).replace(/\n$/, '')

  async function copyCode() {
    try {
      await navigator.clipboard.writeText(source)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2000)
    } catch {
      setCopied(false)
    }
  }

  return (
    <div className="markdown-code-block">
      <div className="markdown-code-block-toolbar">
        <span>{language}</span>
        <button type="button" onClick={() => void copyCode()} aria-label={copied ? t('markdown.copied') : t('markdown.copyCode')} title={copied ? t('markdown.copied') : t('markdown.copyCode')}>
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
          <span>{copied ? t('markdown.copied') : t('markdown.copy')}</span>
        </button>
      </div>
      <SyntaxHighlighter language={language} style={isDarkTheme ? oneDark : oneLight} PreTag="div" customStyle={{ margin: 0 }}>
        {source}
      </SyntaxHighlighter>
    </div>
  )
}

/**
 * Shared markdown renderer used across the chat UI. Renders GitHub-flavored
 * markdown plus LaTeX math (both `$...$`/`$$...$$` and the `\(...\)`/`\[...\]`
 * delimiters some models emit).
 */
export default function Markdown({ children }: Props) {
  return (
    <div className="ds-markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkMath]}
        rehypePlugins={[rehypeKatex]}
        components={{
          pre({ children }) {
            return <>{children}</>
          },
          code({ className, children, ...props }) {
            const isBlock = className?.startsWith('language-') || String(children).endsWith('\n')
            return isBlock
              ? <CodeBlock className={className}>{children}</CodeBlock>
              : <code className={className} {...props}>{children}</code>
          },
        }}
      >
        {normalizeLatexDelimiters(children)}
      </ReactMarkdown>
    </div>
  )
}
