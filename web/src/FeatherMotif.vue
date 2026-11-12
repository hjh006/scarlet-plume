<script setup lang="ts">
// Brand geometry only: no source, risk, or observation is asserted by this motif.
const point = (t: number) => {
  const a = 1 - t
  return { x: a ** 3 * 48 + 3 * a * a * t * 340 + 3 * a * t * t * 650 + t ** 3 * 948,
    y: a ** 3 * 463 + 3 * a * a * t * 330 + 3 * a * t * t * 290 + t ** 3 * 73 }
}
const branches = Array.from({ length: 17 }, (_, n) => n + 1).flatMap(i => {
  const t = i / 19, p = point(t), length = 165 * Math.sin(t * Math.PI) ** .8
  const color = i === 9 ? '#ffb3a6' : i === 12 ? '#d4c4ff' : i % 3 ? '#8eeaf4' : '#9cd8ff'
  return [-1, 1].map(sign => {
    const forward = 90 * (1 - t) + 10
    const x = p.x + forward * .92 + sign * length * .4
    const y = p.y - forward * .4 + sign * length * .92
    return { x, y, color, d: `M${p.x} ${p.y} C${p.x + (sign < 0 ? -length * .08 : 20)} ${p.y + sign * length * .4} ${x - forward * .5} ${y + length * .08} ${x} ${y}` }
  })
})
const nodes = [.03, .22, .39, .57, .77, .94].map(point)
</script>

<template>
  <svg class="feather-motif" viewBox="0 0 1040 540" aria-hidden="true" focusable="false">
    <g v-for="(branch, index) in branches" :key="index" :stroke="branch.color" :fill="branch.color">
      <path :d="branch.d" stroke-width="1.1" opacity=".85" fill="none" />
      <circle :cx="branch.x" :cy="branch.y" r="2.5" stroke="none" />
    </g>
    <path d="M48 463C340 330 650 290 948 73" stroke="#36c1ec" stroke-width="9" opacity=".12" fill="none" />
    <path d="M48 463C340 330 650 290 948 73" stroke="#b7f4ff" stroke-width="2" fill="none" />
    <g v-for="(node,index) in nodes" :key="index" :stroke="index === 3 ? '#ffb3a6' : '#b7f4ff'">
      <circle :cx="node.x" :cy="node.y" r="10" fill="var(--brand-node-surface)" stroke-width="2" />
      <circle :cx="node.x" :cy="node.y" r="3.5" fill="#b4f9ff" stroke="none" />
    </g>
  </svg>
</template>
