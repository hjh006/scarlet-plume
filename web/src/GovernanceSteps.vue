<script setup lang="ts">
defineProps<{ labels: string[]; current: number }>()
import Icon from './Icon.vue'
</script>
<template><nav class="governance-steps" tabindex="0" aria-label="当前工作视角，不代表流程已完成；窄屏可横向滚动"><ol><li v-for="(label,index) in labels" :key="label" :class="{active:index===current}" :aria-current="index===current?'step':undefined"><span>{{index+1}}</span><strong>{{label}}</strong><Icon v-if="index<labels.length-1" name="arrow"/></li></ol></nav></template>
