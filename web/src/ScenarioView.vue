<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Workspace } from './api'
import Icon from './Icon.vue'
import ScenarioDataPanel from './ScenarioDataPanel.vue'
import ScenarioChecksPanel from './ScenarioChecksPanel.vue'
import { useScenarioContext } from './scenario-context'
import { scenarios, scenarioEntryPath } from './scenario-navigation'

const props = defineProps<{ workspace: Workspace; scenarioId: string; taskId?: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const checksPanel = ref<InstanceType<typeof ScenarioChecksPanel>>()
const editor = computed(() => ['owner', 'operator'].includes(props.workspace.role))
const scenario = computed(() => scenarios.find(item => item.id === props.scenarioId))
const task = computed(() => scenario.value?.tasks.find(item => item.id === props.taskId))
const home = computed(() => `#/${encodeURIComponent(props.workspace.slug)}`)
const { href } = useScenarioContext(props.workspace.slug)
const scenarioHref = computed(() => scenarioLink(props.scenarioId))

function scenarioLink(id: string) {
  return href('scenarios/' + encodeURIComponent(id))
}

function taskHref(id: string) {
  return href('scenarios/' + encodeURIComponent(props.scenarioId) + '/' + encodeURIComponent(id))
}

function entryHref(page: string) {
  return href(scenarioEntryPath(props.scenarioId, page), { task: props.taskId ?? '' })
}
</script>

<template>
  <div class="scenario-page">
    <template v-if="!scenarioId">
      <header class="page-header scenario-header"><div><p class="eyebrow">数据安全与合规技术检查</p><h1>场景检查</h1><p>选择检查场景与实际数据范围，运行检查、定位发现、追踪整改并核对新来源复测。</p></div><a class="button button-secondary" :href="href('tools')">数据接入与专业工具<Icon name="chevron"/></a></header>
      <section class="panel" aria-labelledby="scenario-directory-title"><div class="panel-heading"><h2 id="scenario-directory-title">选择技术检查场景</h2></div><ul class="scenario-directory"><li v-for="item in scenarios" :key="item.id"><a :href="scenarioLink(item.id)"><Icon :name="item.icon"/><div><h2>{{ item.label }}</h2><p>{{ item.description }}</p><p class="scenario-directory-trigger"><strong>检查触发</strong>{{ item.trigger }}</p></div><Icon name="chevron"/></a></li></ul></section>
    </template>
    <section v-else-if="!scenario" class="panel scenario-unavailable"><h1>未找到这个检查场景</h1><p>从导航选择已有场景，或返回概览核对已接入数据。</p><a class="button button-primary" :href="href('overview')">返回概览</a></section>
    <template v-else>
      <a v-if="taskId" class="back-link" :href="scenarioHref"><Icon name="back"/>返回{{ scenario.label }}检查</a>
      <header class="page-header scenario-header"><div><p class="eyebrow">技术检查 / {{ scenario.label }}</p><h1>{{ task?.label || scenario.label }}</h1><p>{{ task?.summary || scenario.description }}</p></div><button v-if="editor && (!taskId || task)" class="button button-primary" type="button" :disabled="!checksPanel?.canRun" @click="checksPanel?.run()"><Icon name="checks"/>{{ checksPanel?.running ? '正在运行检查…' : '运行检查' }}</button></header>
      <section v-if="taskId && !task" class="panel scenario-unavailable"><h2>未找到这项技术检查</h2><p>旧任务链接继续兼容已有检查项，请从当前场景重新选择。</p><a class="button button-secondary" :href="scenarioHref">查看本场景检查项</a></section>
      <template v-else>
        <nav class="scenario-task-switcher scenario-check-task-nav" :aria-label="scenario.label + '专项技术检查'"><a :href="scenarioHref" :aria-current="!taskId ? 'page' : undefined">场景综合技术检查</a><a v-for="item in scenario.tasks" :key="item.id" :href="taskHref(item.id)" :aria-current="task?.id === item.id ? 'page' : undefined">{{ item.label }}</a></nav>
        <ScenarioDataPanel :workspace="workspace" :scenario-id="scenarioId" :task-id="taskId" technical @session-expired="emit('sessionExpired')"><template #checks="{scope}"><ScenarioChecksPanel ref="checksPanel" :workspace="workspace" :scenario-id="scenarioId" :task-id="taskId" :asset-id="scope.assetId" :activity-id="scope.activityId" :scope-ready="scope.ready" @session-expired="emit('sessionExpired')"/></template></ScenarioDataPanel>
        <details class="panel scenario-secondary-section"><summary>相关技术台账与辅助证据</summary><div class="scenario-secondary-body"><p>来源台账、分类、处理活动及评估可为本次检查提供补充证据；检查判定以本次采用范围和证据时点为准。</p><p><a :href="href('capabilities?tab=browser')">业务页面字段标注</a> · <a :href="href('capabilities?tab=files')">文件识别证据</a> · <a :href="href('capabilities?tab=value-search')">指纹溯源</a> · <a :href="href('capabilities?tab=reports')">指定范围与周期报告</a></p><ul class="scenario-result-list"><li v-for="view in scenario.views" :key="view.page"><a :href="entryHref(view.page)"><div><h3>{{ view.label }}</h3><p>{{ view.description }}</p></div><Icon name="chevron"/></a></li></ul><ul v-if="task?.entries.length" class="scenario-entry-list"><li v-for="entry in task.entries" :key="entry.page + entry.label"><a :href="entryHref(entry.page)"><div><strong>{{ entry.label }}</strong><p v-if="entry.description">{{ entry.description }}</p></div><Icon name="chevron"/></a></li></ul></div></details>
        <details class="panel scenario-secondary-section"><summary>检查依据、适用条件与报告参考</summary><div class="scenario-secondary-body"><template v-if="task"><h2>适用条件与核查要求</h2><ul class="scenario-guidance-list"><li v-for="requirement in task.requirements" :key="requirement">{{ requirement }}</li></ul><h2>报告与证据参考内容</h2><p>以下为检查范围的参考内容；本次已取得证据和未满足项在检查结果的「监管报告要求」中分别列示。</p><ul class="scenario-material-list"><li v-for="material in task.deliverables" :key="material"><Icon name="reports"/><span>{{ material }}</span></li></ul><p v-if="task.gap" class="scenario-secondary-gap">{{ task.gap }}</p></template><h2>公开依据与版本</h2><ul class="scenario-source-list"><li v-for="source in scenario.sources" :key="source.url + source.label"><a :href="source.url" target="_blank" rel="noopener noreferrer">{{ source.label }}<span class="visually-hidden">（在新窗口打开）</span></a></li></ul></div></details>
      </template>
    </template>
  </div>
</template>

<style scoped>
.scenario-page { display: grid; gap: 20px; min-width: 0; }
.scenario-page > .back-link { justify-self: start; margin: -4px 0 -8px; }
.scenario-header { margin-bottom: 0; }
.scenario-header > div { min-width: 0; }
.scenario-header h1 { overflow-wrap: anywhere; }
.scenario-header .button { flex: none; max-width: 100%; }
.scenario-brief { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); margin: 0; padding: 22px 24px; gap: 24px; }
.scenario-brief > div + div { padding-left: 24px; border-left: 1px solid var(--line); }
.scenario-brief dt { margin-bottom: 9px; color: var(--secondary); font-size: 12px; font-weight: 600; }
.scenario-brief dd { margin: 0; font-size: 14px; line-height: 1.85; overflow-wrap: anywhere; }
.scenario-workbench { display: grid; grid-template-columns: minmax(0, 1fr) 290px; align-items: start; gap: 20px; }
.scenario-main, .scenario-context { display: grid; gap: 20px; min-width: 0; }
.scenario-main > .panel, .scenario-context > .panel { min-width: 0; margin: 0; }
.scenario-directory { list-style: none; margin: 0; padding: 0 24px; }
.scenario-directory li + li { border-top: 1px solid var(--line); }
.scenario-directory a { display: flex; align-items: start; gap: 16px; padding: 23px 0; color: var(--ink); text-decoration: none; }
.scenario-directory a > .icon { flex: none; width: 20px; height: 20px; margin-top: 3px; color: var(--accent); }
.scenario-directory a > .icon:last-child { width: 16px; height: 16px; margin-left: auto; }
.scenario-directory a > div { min-width: 0; }
.scenario-directory h2 { margin: 0; font-size: 16px; line-height: 1.65; }
.scenario-directory a:hover h2 { color: var(--accent); text-decoration: underline; text-underline-offset: 4px; }
.scenario-directory p { margin: 7px 0 0; font-size: 13px; line-height: 1.85; overflow-wrap: anywhere; }
.scenario-directory .scenario-directory-trigger { margin-top: 9px; color: var(--secondary); font-size: 12px; }
.scenario-directory-trigger strong { margin-right: 10px; font-weight: 500; }
.scenario-directory-workspace { display: inline-flex; align-items: center; gap: 6px; min-height: 36px; margin-top: 12px; font-size: 12px; color: var(--accent); }
.scenario-directory-workspace .icon { width: 16px; height: 16px; }
.scenario-task-index, .scenario-guidance-list, .scenario-material-list, .scenario-entry-list, .scenario-source-list { margin: 0; list-style: none; }
.scenario-task-index { padding: 0 22px; }
.scenario-task-index li + li, .scenario-entry-list li + li { border-top: 1px solid var(--line); }
.scenario-task-index a { display: flex; align-items: center; justify-content: space-between; gap: 24px; padding: 23px 2px; color: var(--ink); text-decoration: none; }
.scenario-task-index a > div { min-width: 0; }
.scenario-task-index h3 { margin: 0; font-size: 15px; line-height: 1.65; }
.scenario-task-index p { margin: 8px 0 0; max-width: 70ch; color: var(--secondary); font-size: 13px; line-height: 1.8; overflow-wrap: anywhere; }
.scenario-task-index a:hover h3, .scenario-entry-list a:hover strong { color: var(--accent); text-decoration: underline; text-underline-offset: 4px; }
.scenario-task-open { display: inline-flex; align-items: center; gap: 7px; flex: none; color: var(--accent); font-size: 12px; }
.scenario-task-open .icon, .scenario-entry-list .icon { flex: none; width: 16px; height: 16px; }
.scenario-task-switcher { display: flex; flex-wrap: wrap; gap: 0 20px; border-bottom: 1px solid var(--line); }
.scenario-task-switcher a { display: flex; align-items: center; min-height: 44px; padding: 10px 2px; border-bottom: 2px solid transparent; color: var(--secondary); font-size: 13px; line-height: 1.6; text-decoration: none; }
.scenario-task-switcher a:hover { color: var(--accent); }
.scenario-task-switcher a[aria-current="page"] { border-color: var(--accent); color: var(--accent); font-weight: 600; }
.scenario-result-list { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0 22px; margin: 0; padding: 0 22px; list-style: none; }
.scenario-result-list li { min-width: 0; border-bottom: 1px solid var(--line); }
.scenario-result-list a { display: flex; justify-content: space-between; align-items: center; gap: 16px; min-height: 88px; height: 100%; padding: 17px 0; color: var(--ink); text-decoration: none; }
.scenario-result-list a > div { min-width: 0; }
.scenario-result-list h3 { margin: 0; color: var(--accent); font-size: 14px; line-height: 1.7; }
.scenario-result-list p { margin: 6px 0 0; color: var(--secondary); font-size: 12px; line-height: 1.8; overflow-wrap: anywhere; }
.scenario-result-list .icon { flex: none; width: 16px; height: 16px; color: var(--accent); }
.scenario-result-list a:hover h3 { text-decoration: underline; text-underline-offset: 4px; }
.scenario-guidance-list { display: grid; gap: 17px; padding: 22px 24px 24px 42px; list-style: disc; font-size: 14px; line-height: 1.9; }
.scenario-guidance-list li { padding-left: 3px; overflow-wrap: anywhere; }
.scenario-guidance-list li::marker { color: var(--secondary); }
.scenario-material-list { display: grid; gap: 0; padding: 5px 24px; }
.scenario-material-list li { display: flex; align-items: start; gap: 12px; padding: 17px 0; font-size: 14px; line-height: 1.8; overflow-wrap: anywhere; }
.scenario-material-list li + li { border-top: 1px solid var(--line); }
.scenario-material-list .icon { flex: none; width: 18px; height: 18px; margin-top: 4px; color: var(--secondary); }
.scenario-capability { background: var(--surface-subtle); }
.scenario-capability > p { margin: 0; padding: 20px 24px; font-size: 14px; line-height: 1.9; overflow-wrap: anywhere; }
.scenario-entry-list { padding: 0 24px; }
.scenario-entry-list a { display: flex; justify-content: space-between; align-items: center; gap: 20px; min-height: 62px; padding: 17px 0; color: var(--accent); text-decoration: none; }
.scenario-entry-list strong { font-size: 14px; line-height: 1.7; }
.scenario-entry-list p { margin: 6px 0 0; color: var(--secondary); font-size: 13px; line-height: 1.8; overflow-wrap: anywhere; }
.scenario-no-entry { margin: 0; padding: 22px 24px; color: var(--secondary); font-size: 14px; line-height: 1.8; }
.scenario-context .panel-heading h2 { font-size: 15px; }
.scenario-context-body { margin: 0; padding: 18px 20px; font-size: 13px; line-height: 1.9; overflow-wrap: anywhere; }
.scenario-context-body > p { margin: 0; }
.scenario-context-body dl { padding-top: 16px; margin: 18px 0 0; border-top: 1px solid var(--line); }
.scenario-context-body dt { margin-bottom: 7px; color: var(--secondary); font-size: 12px; }
.scenario-context-body dd { margin: 0; }
.scenario-source-list { display: grid; gap: 0; padding: 3px 20px; }
.scenario-source-list li { padding: 15px 0; font-size: 12px; line-height: 1.9; overflow-wrap: anywhere; }
.scenario-source-list li + li { border-top: 1px solid var(--line); }
.scenario-source-list a { color: var(--accent); text-underline-offset: 4px; }
.scenario-reading-note { padding: 20px; background: var(--surface-subtle); }
.scenario-reading-note h2 { margin: 0 0 12px; font-size: 14px; }
.scenario-reading-note p { margin: 10px 0 0; color: var(--secondary); font-size: 12px; line-height: 1.9; }
.scenario-unavailable { display: grid; justify-items: start; gap: 16px; padding: 28px; }
.scenario-unavailable h1, .scenario-unavailable h2, .scenario-unavailable p { margin: 0; }
.scenario-unavailable p { color: var(--secondary); line-height: 1.85; }
@media (min-width: 1100px) and (max-width: 1399px) {
  .scenario-workbench { grid-template-columns: minmax(0, 1fr) 260px; gap: 16px; }
  .scenario-main, .scenario-context { gap: 16px; }
  .scenario-task-index a { gap: 18px; }
  .scenario-task-switcher { column-gap: 16px; }
  .scenario-result-list { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 1099px) {
  .scenario-workbench { grid-template-columns: minmax(0, 1fr); }
  .scenario-context { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 699px) {
  .scenario-brief, .scenario-context { grid-template-columns: minmax(0, 1fr); }
  .scenario-brief > div + div { padding: 20px 0 0; border-left: 0; border-top: 1px solid var(--line); }
  .scenario-task-index a { align-items: start; gap: 16px; }
  .scenario-task-open { font-size: 0; }
  .scenario-task-open .icon { width: 18px; height: 18px; }
  .scenario-header { flex-wrap: wrap; }
  .scenario-result-list { grid-template-columns: minmax(0, 1fr); }
}
</style>

<style scoped>.scenario-check-task-nav{flex-wrap:wrap}.scenario-secondary-section>summary{padding:17px 20px;color:var(--accent);font-size:13px;font-weight:650;cursor:pointer}.scenario-secondary-body{padding:0 20px 20px;font-size:12px;line-height:1.8}.scenario-secondary-body>p{color:var(--secondary);margin-bottom:12px}.scenario-secondary-body>h2{font-size:14px;margin:18px 0 10px}.scenario-secondary-body .scenario-result-list,.scenario-secondary-body .scenario-entry-list{padding:0}.scenario-secondary-body .scenario-result-list{grid-template-columns:repeat(2,minmax(0,1fr))}.scenario-secondary-body .scenario-source-list{padding:0}.scenario-secondary-gap{padding:12px 14px;background:var(--surface-subtle);border-radius:6px;margin-top:16px}@media(max-width:767px){.scenario-secondary-body .scenario-result-list{grid-template-columns:minmax(0,1fr)}}</style>
