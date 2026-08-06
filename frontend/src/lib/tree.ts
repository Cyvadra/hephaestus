import type { ChatMessage } from '../api/types'

// Build a {parentId → children[]} map from the flat list.
export function buildChildrenMap(messages: ChatMessage[]): Map<number | null, ChatMessage[]> {
  const map = new Map<number | null, ChatMessage[]>()
  for (const m of messages) {
    const key = m.ParentMessageID ?? null
    if (!map.has(key)) map.set(key, [])
    map.get(key)!.push(m)
  }
  return map
}

// Walk from leafId up to the root, returning the path root→leaf.
export function activePath(
  leafId: number | null,
  byId: Map<number, ChatMessage>,
): ChatMessage[] {
  if (leafId == null) return []
  const path: ChatMessage[] = []
  let cur: ChatMessage | undefined = byId.get(leafId)
  while (cur) {
    path.unshift(cur)
    cur = cur.ParentMessageID != null ? byId.get(cur.ParentMessageID) : undefined
  }
  return path
}

// Returns siblings of msg (all children of its parent), sorted by ID.
export function siblings(
  msg: ChatMessage,
  childrenMap: Map<number | null, ChatMessage[]>,
): ChatMessage[] {
  return (childrenMap.get(msg.ParentMessageID ?? null) ?? []).slice().sort((a, b) => a.ID - b.ID)
}

// Follow the deepest latest-ID chain from a given message down to a leaf.
export function descendToLeaf(
  startId: number,
  childrenMap: Map<number | null, ChatMessage[]>,
): number {
  let id = startId
  while (true) {
    const kids = childrenMap.get(id)
    if (!kids || kids.length === 0) return id
    // pick the child with the highest ID (most recent branch)
    id = kids.reduce((best, c) => (c.ID > best.ID ? c : best)).ID
  }
}

export function buildById(messages: ChatMessage[]): Map<number, ChatMessage> {
  return new Map(messages.map((m) => [m.ID, m]))
}
