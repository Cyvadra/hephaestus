import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import { normalizeLatexDelimiters } from '../lib/markdown'

interface Props {
  children: string
}

/**
 * Shared markdown renderer used across the chat UI. Renders GitHub-flavored
 * markdown plus LaTeX math (both `$...$`/`$$...$$` and the `\(...\)`/`\[...\]`
 * delimiters some models emit).
 */
export default function Markdown({ children }: Props) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm, remarkMath]}
      rehypePlugins={[rehypeKatex]}
    >
      {normalizeLatexDelimiters(children)}
    </ReactMarkdown>
  )
}
