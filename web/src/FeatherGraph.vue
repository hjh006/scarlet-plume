<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import type { DataFlowGraph, DataFlowNode } from './data-flows'
import { dataFlowNodeLabels, dataFlowRelationLabels } from './data-flows'
import { featherLayout, type FeatherNode } from './graph-layout'

const props = defineProps<{ graph: DataFlowGraph | null; loading?: boolean; selected?: string; compact?: boolean }>()
const emit = defineEmits<{ select: [node: DataFlowNode] }>()
const id = useId().replace(/[^a-zA-Z0-9_-]/g, '')
const layout = computed(() => featherLayout(props.graph))
const zoom = ref(1), pan = ref({ x: 0, y: 0 })
const hoveredId = ref(''), focusedId = ref('')
let start: { x: number; y: number; panX: number; panY: number; unit: number } | undefined
const adjacent = computed(() => new Set(props.graph?.edges.filter(edge => edge.from === props.selected || edge.to === props.selected).flatMap(edge => [edge.from, edge.to]) ?? []))
const selectedNode = computed(() => props.graph?.nodes.find(node => node.id === props.selected))
function reset() { zoom.value = 1; pan.value = { x: 0, y: 0 } }
function scale(delta: number) { zoom.value = Math.max(.7, Math.min(2.5, Math.round((zoom.value + delta) * 10) / 10)) }
function pointerDown(event: PointerEvent) {
  if (event.button !== 0 || (event.target as Element).closest('[data-graph-node]')) return
  const element = event.currentTarget as SVGSVGElement
  element.setPointerCapture(event.pointerId)
  start = { x: event.clientX, y: event.clientY, panX: pan.value.x, panY: pan.value.y, unit: 1040 / element.getBoundingClientRect().width }
}
function pointerMove(event: PointerEvent) {
  if (!start) return
  const dx = (event.clientX - start.x) * start.unit, dy = (event.clientY - start.y) * start.unit
  pan.value = { x: start.panX + dx, y: start.panY + dy }
}
function pointerUp() { start = undefined }
function choose(node: DataFlowNode) { emit('select', node) }
function textWidth(value: string) { return [...value].reduce((width, character) => width + (character.charCodeAt(0) > 255 ? 11 : 6.4), 0) }
function label(node: DataFlowNode) { let value = ''; for (const character of node.label) { if (textWidth(value + character) > 150) return value + '…'; value += character } return value }
function labelWidth(node: DataFlowNode) { return Math.max(62, Math.min(190, textWidth(label(node)) + 20)) }
function labelOffset(item: FeatherNode) { return item.disconnected ? 470 - item.y : item.labelAbove ? -38 : 22 }
// Keep the active label readable; suppress a default shaft label if its box collides.
// Other node names remain available on hover, selection and keyboard focus.
const visibleLabels = computed(() => {
  const activeId = hoveredId.value || focusedId.value || props.selected
  const active = layout.value.nodes.find(item => item.node.id === activeId)
  const candidates = [...(active ? [active] : []), ...layout.value.nodes.filter(item => item.shaft && item.node.id !== activeId)]
  const boxes: { left: number; right: number; top: number; bottom: number }[] = [], visible = new Set<string>()
  for (const item of candidates) {
    const width = labelWidth(item.node), top = item.y + labelOffset(item) - 4
    const box = { left: item.x - width / 2, right: item.x + width / 2, top, bottom: top + 25 }
    if (boxes.some(other => box.left < other.right + 8 && box.right + 8 > other.left && box.top < other.bottom + 6 && box.bottom + 6 > other.top)) continue
    boxes.push(box); visible.add(item.node.id)
  }
  return visible
})
watch(() => props.graph, () => { reset(); hoveredId.value = ''; focusedId.value = '' })
</script>

<template>
  <div class="feather-graph" :class="{ compact }" :aria-busy="loading">
    <svg v-if="layout.nodes.length" class="feather-canvas" viewBox="0 0 1040 620" role="group" aria-label="有来源依据的数据关系图，可选择节点或使用右下角缩放控件" @pointerdown="pointerDown" @pointermove="pointerMove" @pointerup="pointerUp" @pointercancel="pointerUp">
      <defs>
        <linearGradient :id="id + '-line'" x1="0%" y1="100%" x2="100%" y2="0%"><stop stop-color="#b2f4ff" /><stop offset="1" stop-color="#68caff" /></linearGradient>
        <marker :id="id + '-arrow'" markerWidth="6" markerHeight="6" refX="8" refY="3" orient="auto"><path d="M0 0 6 3 0 6" fill="none" stroke="currentColor" /></marker>
      </defs>
      <g :transform="`translate(${520 + pan.x} ${310 + pan.y}) scale(${zoom}) translate(-520 -310)`">
        <text v-if="layout.disconnected" x="70" y="508" class="feather-detached-label">{{ layout.disconnected }} 个节点尚无已验证的关联</text>
        <path v-for="item in layout.edges" :key="item.edge.id" :d="item.path" class="feather-edge" :class="{ shaft: item.shaft, subdued: selected && item.edge.from !== selected && item.edge.to !== selected, declared: ['catalog', 'declared', 'database_type'].includes(item.edge.relation) }" :data-relation="item.edge.relation" :stroke="item.shaft ? `url(#${id}-line)` : undefined" :marker-end="`url(#${id}-arrow)`"><title>{{ dataFlowRelationLabels[item.edge.relation] }} · {{ item.edge.evidence?.length || 0 }}项来源记录</title></path>
        <g v-for="item in layout.nodes" :key="item.node.id" data-graph-node class="feather-node" :class="{ selected: selected === item.node.id, neighbor: adjacent.has(item.node.id), shaft: item.shaft, subdued: selected && selected !== item.node.id && !adjacent.has(item.node.id) }" :transform="`translate(${item.x} ${item.y})`" tabindex="0" role="button" :aria-label="`${item.node.label}，${dataFlowNodeLabels[item.node.kind]}，查看依据`" :aria-pressed="selected === item.node.id" @mouseenter="hoveredId = item.node.id" @mouseleave="hoveredId = ''" @focus="focusedId = item.node.id" @blur="focusedId = ''" @click.stop="choose(item.node)" @keydown.enter.prevent="emit('select', item.node)" @keydown.space.prevent="emit('select', item.node)">
          <title>{{ item.node.label }} · {{ dataFlowNodeLabels[item.node.kind] }}</title>
          <circle class="feather-halo" :r="item.shaft ? 12 : 8" /><circle class="feather-point" :r="item.shaft ? 5 : 3.5" />
          <g v-if="visibleLabels.has(item.node.id)" class="feather-label" :class="{ secondary: !item.shaft }" :transform="`translate(0 ${labelOffset(item)})`">
            <rect :x="-labelWidth(item.node) / 2" y="-4" :width="labelWidth(item.node)" height="25" rx="5" />
            <text y="13" text-anchor="middle">{{ label(item.node) }}</text>
          </g>
        </g>
      </g>
    </svg>
    <div v-else class="feather-empty" role="status"><strong>{{ loading ? '正在关联数据关系…' : '当前范围内暂无关系' }}</strong><p>{{ loading ? '按已授权来源读取节点、关系与证据。' : '接入来源或清除筛选后，可查看有实际依据的关系。' }}</p></div>
    <div class="feather-scope"><span class="feather-key confirmed">实际观测与定位</span><span class="feather-key declared">目录与契约声明</span><span v-if="graph && graph.nodes.length > 60">图示前60个节点，完整范围见台账</span><span v-else>{{ layout.nodes.length }}节点 · {{ layout.edges.length }}关系</span></div>
    <div class="feather-tools" aria-label="图谱缩放"><button type="button" aria-label="放大关系图" :disabled="zoom >= 2.5" @click="scale(.2)">+</button><output aria-label="缩放比例">{{ Math.round(zoom * 100) }}%</output><button type="button" aria-label="缩小关系图" :disabled="zoom <= .7" @click="scale(-.2)">−</button><button type="button" aria-label="重置关系图位置与缩放" @click="reset">重置</button></div>
    <p class="visually-hidden" aria-live="polite">{{ selectedNode ? '已选择' + selectedNode.label : '选择节点查看来源依据；拖动画布调整位置。' }}</p>
  </div>
</template>

<style scoped>
.feather-graph{position:relative;min-width:0;min-height:480px;background:var(--brand-gradient);color:#edf5ff;overflow:hidden;border-radius:0 0 12px 12px;isolation:isolate}.feather-graph.compact{min-height:390px}.feather-canvas{display:block;width:100%;height:100%;min-height:480px;max-height:630px;touch-action:none;cursor:grab}.compact .feather-canvas{min-height:390px;max-height:530px}.feather-canvas:active{cursor:grabbing}.feather-edge{fill:none;stroke:#7ceaf5;stroke-width:1.25;opacity:1;color:#7ceaf5;transition:stroke 150ms}.feather-edge.shaft{stroke-width:2.4;opacity:1;filter:drop-shadow(0 1px 3px #2bc9de70)}.feather-edge.declared{stroke-dasharray:4 4;stroke:#c0e5ff;opacity:1}.feather-node{cursor:pointer;outline:none}.feather-halo{fill:#15537f;stroke:#b1e8f4;stroke-width:1.5}.feather-point{fill:#c2f7ff}.feather-node:not(.shaft) .feather-halo{fill:#15537f;stroke:#bddff5}.feather-node:not(.shaft) .feather-point{fill:#c5e9ff}.feather-detached-label{font-size:11px;fill:#edf5ff}.feather-node.selected .feather-halo,.feather-node:focus-visible .feather-halo{stroke:#ffb3a6;stroke-width:3;fill:#452735}.feather-node.selected .feather-point{fill:#ffb3a6}.feather-label rect{fill:#154b7d;stroke:#adddf5;stroke-width:1}.feather-label text{font-family:inherit;font-size:11px;font-weight:600;fill:#f1f7ff;pointer-events:none}.feather-label.secondary text{font-size:10px;font-weight:400}.feather-node.selected .feather-label rect,.feather-node:focus-visible .feather-label rect{stroke:#ffb3a6}.feather-node.subdued .feather-halo{stroke:#c0e5ff}.feather-node.subdued .feather-point{fill:#d5efff}.feather-edge.subdued{stroke:#b5dbea;color:#b5dbea;opacity:1}.feather-edge.shaft.subdued{stroke:#b5dbea;color:#b5dbea;opacity:1}.feather-node:hover,.feather-node:focus-visible,.feather-node.selected{opacity:1}.feather-node:hover .feather-label rect{stroke:#a9e8f6}.feather-scope{position:absolute;bottom:15px;left:18px;right:88px;display:flex;flex-wrap:wrap;align-items:center;gap:9px 16px;font-size:10px;color:#edf5ff;pointer-events:none}.feather-key{display:inline-flex;align-items:center;gap:5px}.feather-key::before{content:'';width:19px;border-top:2px solid #7ceaf5}.feather-key.declared::before{border-top:1px dashed #c0e5ff}.feather-tools{position:absolute;right:18px;bottom:34px;display:grid;width:43px;border:1px solid #a8d4eb;background:#154b7d;border-radius:7px;overflow:hidden}.feather-tools button{min-height:33px;border:0;background:#f9fcff;color:#182a47;font-size:18px;cursor:pointer}.feather-tools button:last-child{font-size:10px}.feather-tools button:hover{background:#dceffc}.feather-tools button:disabled{color:#8fa0b6;cursor:default}.feather-tools output{text-align:center;padding:4px 0;font-size:9px;color:#f1f8ff}.feather-empty{display:grid;place-content:center;text-align:center;min-height:480px;padding:35px}.feather-empty strong{font-size:17px;color:#e4f1ff}.feather-empty p{font-size:12px;max-width:44ch;color:#edf5ff;margin-top:12px}@media(prefers-reduced-motion:reduce){.feather-edge{transition:none}}
</style>
