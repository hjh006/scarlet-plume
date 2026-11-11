import type { DataFlowEdge, DataFlowGraph, DataFlowNode } from './data-flows'

export interface FeatherNode { node: DataFlowNode; x: number; y: number; shaft: boolean; labelAbove: boolean; disconnected: boolean; labelled: boolean }
export interface FeatherEdge { edge: DataFlowEdge; path: string; shaft: boolean }

// The shaft is an actual directed path. Unrelated catalog nodes remain detached.
// Complete records stay in the ledger when the 60-node drawing budget is reached.
export function featherLayout(graph: DataFlowGraph | null | undefined) {
  const nodes = (graph?.nodes ?? []).slice(0, 60)
  const known = new Set(nodes.map(node => node.id))
  const edges = (graph?.edges ?? []).filter(edge => known.has(edge.from) && known.has(edge.to))
  const next = new Map<string, string[]>(), previous = new Map<string, string[]>()
  for (const edge of edges) {
    next.set(edge.from, [...new Set([...(next.get(edge.from) ?? []), edge.to])])
    previous.set(edge.to, [...new Set([...(previous.get(edge.to) ?? []), edge.from])])
  }
  const roots = nodes.filter(node => !previous.has(node.id) && next.has(node.id))
  const starts = roots.length ? roots : nodes.filter(node => next.has(node.id)).slice(0, 1)
  let shaft: string[] = []
  for (const start of starts) {
    const distance = new Map<string, number>([[start.id, 0]]), parent = new Map<string, string>()
    const queue = [start.id]
    for (let cursor = 0; cursor < queue.length; cursor++) {
      for (const target of next.get(queue[cursor]) ?? []) {
        if (!distance.has(target)) { distance.set(target, (distance.get(queue[cursor]) ?? 0) + 1); parent.set(target, queue[cursor]); queue.push(target) }
      }
    }
    const tip = queue.reduce((best, id) => (distance.get(id) ?? 0) > (distance.get(best) ?? 0) ? id : best, start.id)
    const candidate: string[] = []
    for (let id = tip; id; id = parent.get(id) ?? '') candidate.unshift(id)
    if (candidate.length > shaft.length) shaft = candidate
  }
  const shaftSet = new Set(shaft), positions = new Map<string, FeatherNode>()
  shaft.forEach((id, index) => {
    const progress = shaft.length > 1 ? index / (shaft.length - 1) : .5
    positions.set(id, { node: nodes.find(node => node.id === id)!, x: 125 + progress * 730, y: 450 - progress * 290, shaft: true, labelAbove: true, disconnected: false, labelled: true })
  })
  const branches = nodes.filter(node => !shaftSet.has(node.id) && (next.has(node.id) || previous.has(node.id)))
  branches.forEach((node, index) => {
    // Even spacing prevents dense roots from occupying the same coordinates.
    // Placement alone has no relationship semantics; every rendered edge is supplied.
    const progress = (index + 1) / (branches.length + 1), above = index % 2 === 0
    const offset = 90 + Math.floor(index / 2) % 3 * 34
    const x = 100 + progress * 820, y = Math.max(65, Math.min(475, 450 - progress * 290 + (above ? -offset : offset)))
    positions.set(node.id, { node, x, y, shaft: false, labelAbove: above, disconnected: false, labelled: node.kind === 'application' || node.kind === 'subject' })
  })
  const detached = nodes.filter(node => !next.has(node.id) && !previous.has(node.id))
  const columns = Math.min(12, Math.max(1, detached.length))
  detached.forEach((node, index) => positions.set(node.id, { node, x: 85 + (index % columns) * (850 / Math.max(1, columns - 1)), y: 535 + Math.floor(index / columns) * Math.min(28, 48 / Math.max(1, Math.ceil(detached.length / columns) - 1)), shaft: false, labelAbove: true, disconnected: true, labelled: false }))
  const plotted = nodes.map(node => positions.get(node.id)!)
  const plottedEdges: FeatherEdge[] = edges.map(edge => {
    const from = positions.get(edge.from)!, to = positions.get(edge.to)!
    const isShaft = shaft.some((id, index) => id === edge.from && shaft[index + 1] === edge.to)
    const dx = to.x - from.x, dy = to.y - from.y
    const bow = isShaft ? 0 : (from.labelAbove ? -1 : 1) * Math.min(80, 25 + Math.abs(dx) * .1)
    return { edge, shaft: isShaft, path: `M ${from.x} ${from.y} C ${from.x + dx * .3} ${from.y + dy * .2 + bow}, ${from.x + dx * .7} ${from.y + dy * .8 + bow}, ${to.x} ${to.y}` }
  })
  return { nodes: plotted, edges: plottedEdges, shaft: shaftSet, disconnected: detached.length }
}
