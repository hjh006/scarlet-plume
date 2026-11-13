<script setup lang="ts">
import type { Workspace } from './api'
import Icon from './Icon.vue'
import { configurationToolGroups } from './navigation'
import { scenarios } from './scenario-navigation'

defineProps<{ workspace: Workspace }>()
</script>

<template>
  <div class="tools-view">
    <header class="page-header">
      <div>
        <p class="eyebrow">工作区 / 专业工具与配置</p>
        <h1>专业工具与配置</h1>
        <p>配置数据来源、采集任务、检测策略与检查规则。业务结果和台账请在对应场景查看。</p>
      </div>
      <a class="button button-primary" :href="`#/${encodeURIComponent(workspace.slug)}/scenarios`"><Icon name="checks" />按场景检查</a>
    </header>
    <div class="tools-sections">
      <section v-for="group in configurationToolGroups" :key="group.label" class="panel">
        <div class="panel-heading"><h2>{{ group.label }}</h2></div>
        <ul class="tool-list">
          <li v-for="tool in group.items" :key="tool.id">
            <a :href="`#/${encodeURIComponent(workspace.slug)}/${tool.id}`">
              <Icon :name="tool.icon" />
              <span><strong>{{ tool.label }}</strong><small>{{ tool.description }}</small></span>
              <Icon name="chevron" />
            </a>
          </li>
        </ul>
      </section>
    </div>
    <section class="panel tools-business-scenes" aria-labelledby="tools-business-scenes-title">
      <div class="panel-heading"><div><h2 id="tools-business-scenes-title">业务结果按场景查看</h2><p>从相应场景进入资产、分类分级、图谱、检查及评估等业务页面。</p></div></div>
      <ul class="tools-scene-links">
        <li v-for="scenario in scenarios" :key="scenario.id"><a :href="`#/${encodeURIComponent(workspace.slug)}/scenarios/${scenario.id}`"><Icon :name="scenario.icon" /><span>{{ scenario.label }}</span><Icon name="chevron" /></a></li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.tools-view { display: grid; gap: 20px; min-width: 0; }
.tools-view .page-header { margin: 0; }
.tools-sections { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: 16px; align-items: start; }
.tool-list { list-style: none; margin: 0; padding: 0; }
.tool-list li + li { border-top: 1px solid var(--line); }
.tool-list a { display: flex; align-items: center; gap: 13px; padding: 17px 18px; text-decoration: none; color: var(--ink); }
.tool-list a:hover { background: var(--surface-subtle); }
.tool-list a > .icon:first-child { width: 20px; height: 20px; color: var(--accent); }
.tool-list a > span { flex: 1; min-width: 0; }
.tool-list strong { display: block; font-size: 14px; font-weight: 700; }
.tool-list small { display: block; margin-top: 5px; color: var(--secondary); font-size: 12px; line-height: 1.75; }
.tool-list a > .icon:last-child { color: var(--secondary); }
.tools-scene-links { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 22px; margin: 0; padding: 6px 20px; list-style: none; }
.tools-scene-links li { min-width: 0; border-bottom: 1px solid var(--line); }
.tools-scene-links a { display: flex; align-items: center; gap: 12px; min-height: 52px; padding: 12px 0; color: var(--accent); text-decoration: none; font-size: 13px; }
.tools-scene-links a:hover span { text-decoration: underline; text-underline-offset: 4px; }
.tools-scene-links span { flex: 1; min-width: 0; }
.tools-scene-links .icon { flex: none; width: 17px; height: 17px; }
.tools-scene-links .icon:last-child { width: 15px; height: 15px; }
@media(max-width:1100px) { .tools-sections { grid-template-columns: minmax(0,1fr); } }
@media(max-width:699px) { .tools-scene-links { grid-template-columns: minmax(0,1fr); } }
</style>
