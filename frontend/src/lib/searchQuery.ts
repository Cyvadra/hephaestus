const KEYWORD_QUERY_MAX_CHARS = 50
const KEYWORD_QUERY_MAX_DELIMITERS = 3

/**
 * Mirrors session.ParseSearchQuery: a short query (< 50 chars) containing
 * spaces or ASCII commas, no other punctuation, and at most three delimiter
 * runs is split on /[, ]+/ into keywords that must all match. Returns null
 * for an ordinary phrase query.
 */
export function parseKeywordQuery(raw: string): string[] | null {
  const query = raw.trim()
  if (!query || Array.from(query).length >= KEYWORD_QUERY_MAX_CHARS || !/[ ,]/.test(query)) return null
  if (/[^\P{P},]|\p{S}|[^\S ]/u.test(query)) return null
  const keywords = query.replace(/^[, ]+|[, ]+$/g, '').split(/[, ]+/)
  if (keywords.length < 2 || keywords.length - 1 > KEYWORD_QUERY_MAX_DELIMITERS) return null
  return keywords
}
