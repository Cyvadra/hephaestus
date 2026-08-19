import { useState, type ComponentPropsWithoutRef } from 'react'
import { Check, Copy } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter'
import { oneLight } from 'react-syntax-highlighter/dist/esm/styles/prism'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import { normalizeLatexDelimiters } from '../lib/markdown'

interface Props {
  children: string
}

type CodeProps = ComponentPropsWithoutRef<'code'>

function CodeBlock({ className, children }: CodeProps) {
  const [copied, setCopied] = useState(false)
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
        <button type="button" onClick={() => void copyCode()} aria-label={copied ? 'Copied' : 'Copy code'} title={copied ? 'Copied' : 'Copy code'}>
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
          <span>{copied ? 'Copied' : 'Copy'}</span>
        </button>
      </div>
      <SyntaxHighlighter language={language} style={oneLight} PreTag="div" customStyle={{ margin: 0 }}>
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
