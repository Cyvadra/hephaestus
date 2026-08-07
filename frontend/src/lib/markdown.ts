// Markdown helpers for the chat renderer.
//
// Some models emit LaTeX with \( ... \) (inline) and \[ ... \] (display)
// delimiters. `remark-math` (v6) only parses `$ ... $` / `$$ ... $$`, and
// worse, CommonMark consumes `\(` / `\[` as backslash escapes during parsing —
// so by the time a remark plugin runs the delimiters are already gone.
//
// We therefore normalize the RAW markdown string *before* it is parsed,
// converting `\(...\)` -> `$...$` and `\[...\]` -> `$$...$$`. Code blocks and
// inline code spans are left untouched so backslashes in code are safe.

// Regions that must never be touched: fenced code blocks (``` or ~~~) and
// inline code spans (`...`). Used with split() so that odd indices hold the
// protected regions and even indices hold transformable prose.
const PROTECTED = /(`{3,}[\s\S]*?`{3,}|~{3,}[\s\S]*?~{3,}|`[^`\n]*`)/g

// Display math that starts at the beginning of a line and ends at a line
// boundary: convert it to a $$ ... $$ block.
const DISPLAY_BLOCK = /^[ \t]*\\\[([\s\S]*?)\\\][ \t]*(?=\n|$)/gm

// Any \[ ... \] that is not line-delimited (embedded mid-sentence): treat as
// inline math so the sentence is not torn apart.
const DISPLAY_INLINE = /\\\[([\s\S]*?)\\\]/g

// Inline math \( ... \) (single line).
const INLINE = /\\\((.*?)\\\)/g

function transformLatex(text: string): string {
  // Emit display math in the canonical block form (delimiters on their own
  // lines) so remark-math/rehype-katex render it as a display block.
  let out = text.replace(DISPLAY_BLOCK, (_, tex: string) => `\n\n$$\n${tex.trim()}\n$$\n\n`)
  out = out.replace(DISPLAY_INLINE, (_, tex: string) => `$${tex}$`)
  out = out.replace(INLINE, (_, tex: string) => `$${tex}$`)
  return out
}

/**
 * Convert `\( ... \)` / `\[ ... \]` LaTeX delimiters in raw markdown into the
 * `$ ... $` / `$$ ... $$` forms understood by `remark-math`, skipping any
 * fenced code blocks and inline code spans.
 */
export function normalizeLatexDelimiters(markdown: string): string {
  const segments = markdown.split(PROTECTED)
  return segments
    .map((segment, index) => (index % 2 === 1 ? segment : transformLatex(segment)))
    .join('')
}
