<script setup lang="ts">
import { computed, defineAsyncComponent, h, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ApiError, describeError, request, roleLabels, sessionPath, type Session } from './api'
import Brand from './Brand.vue'
import ScenarioView from './ScenarioView.vue'
import ToolsView from './ToolsView.vue'
import CapabilityToolsView from './CapabilityToolsView.vue'
import { primaryScenarioForPage, scenarioEntryPath, scenarioOwnsPage, scenarios } from './scenario-navigation'
import { configurationToolGroups, toolGroups, toolPages } from './navigation'
import { readScenarioContext, scenarioContextHref, scenarioScopeEvent } from './scenario-context'
import phoenixFeather from './assets/phoenix-feather.webp'
import Icon from './Icon.vue'
import WorkspaceView from './WorkspaceView.vue'
import PostgresInspectionView from './PostgresInspectionView.vue'
import ChecksView from './ChecksView.vue'
import CheckCreate from './CheckCreate.vue'
import CheckDetail from './CheckDetail.vue'
import ActivitiesView from './ActivitiesView.vue'
import ActivityCreate from './ActivityCreate.vue'
import ActivityDetail from './ActivityDetail.vue'
import TemplatesView from './TemplatesView.vue'
import TemplateForm from './TemplateForm.vue'
import ComplianceReportView from './ComplianceReportView.vue'
import ReportArchiveView from './ReportArchiveView.vue'
import AutomationView from './AutomationView.vue'
import ApiSecurityView from './ApiSecurityView.vue'
import ApiRuntimeView from './ApiRuntimeView.vue'
import ApiLifecycleView from './ApiLifecycleView.vue'
import DataFlowsView from './DataFlowsView.vue'
import RuntimePoliciesView from './RuntimePoliciesView.vue'
import DiscoveryView from './DiscoveryView.vue'
import AssetCatalogView from './AssetCatalogView.vue'
import SecurityOperationsView from './SecurityOperationsView.vue'
import AssessmentsView from './AssessmentsView.vue'
import AuditView from './AuditView.vue'
import ClassificationView from './ClassificationView.vue'
import ClassificationStandardsView from './ClassificationStandardsView.vue'

const ApiValidationView = defineAsyncComponent({
  loader: () => import('./ApiValidationView.vue'),
  delay: 0,
  timeout: 15000,
  loadingComponent: () => h('p', { class: 'message', role: 'status' }, '正在载入 API 验证页面…'),
  errorComponent: () => h('p', { class: 'message message-error', role: 'alert' }, '页面资源未能载入，请刷新页面重试。'),
})

const pages = [...toolPages,
  { id: 'scenarios', label: '场景检查', icon: 'checks' },
  { id: 'tools', label: '专业工具与配置', icon: 'templates' },
] as const

interface NavigationEntry { path: string; label: string; group: string; icon: string; keywords: string }
const scenarioEntries: NavigationEntry[] = scenarios.flatMap(scenario => [
  { path: `scenarios/${scenario.id}`, label: scenario.label, group: '场景检查', icon: scenario.icon, keywords: scenario.description + scenario.trigger + (scenario.id === 'domestic' ? ' 国内监管 国内监管要求' : '') },
  ...scenario.views.map(view => ({ path: scenarioEntryPath(scenario.id, view.page), label: view.label, group: `${scenario.label} · 结果与台账`, icon: toolPages.find(item => item.id === view.page.split(/[/?]/)[0])?.icon || scenario.icon, keywords: view.description })),
  ...scenario.tasks.map(task => ({ path: task.canonicalPath || `scenarios/${scenario.id}/${task.id}`, label: task.label, group: `${scenario.label} · 专项技术检查`, icon: scenario.icon, keywords: task.summary })),
])
const scenarioNavigation = [...new Map(scenarioEntries.map(item => [item.path, item] as const)).values()]
const toolNavigation: NavigationEntry[] = toolGroups.flatMap(group => group.items.filter(item => !primaryScenarioForPage(item.id)).map(item => ({
  path: item.id, label: item.id === 'overview' ? '概览' : item.label, group: group.label, icon: item.icon, keywords: item.description,
})))
const directoryNavigation: NavigationEntry = { path: 'tools', label: '专业工具与配置', group: '工作区', icon: 'templates', keywords: '采集 接入 检查 模板 规则 配置' }

function readRoute() {
  const [path, query = ''] = window.location.hash.split('?')
  const [, rawSlug, requestedPage, recordId = '', contextId = ''] = path!.split('/')
  const parameters = new URLSearchParams(query)
  let slug = ''
  try {
    slug = decodeURIComponent(rawSlug || '')
  } catch {
    slug = ''
  }
  const page = pages.find(page => page.id === requestedPage)?.id || 'overview'
  return { slug, page, recordId, contextId, sceneId: parameters.get('scene') || '', scopeAssetId: parameters.get('assetId') || '', scopeActivityId: parameters.get('activityId') || '', capabilityTab:parameters.get('tab')||'', explicitScene: parameters.has('scene') }
}

function attachSceneContext(target: ReturnType<typeof readRoute>, sceneId: string) {
  target.sceneId = sceneId
  target.explicitScene = true
  const url = new URL(window.location.href)
  const [path, query = ''] = url.hash.split('?')
  const parameters = new URLSearchParams(query)
  parameters.set('scene', sceneId)
  url.hash = `${path}?${parameters}`
  window.history.replaceState(window.history.state, '', url)
}

const initialRoute = readRoute()
const initialScene = primaryScenarioForPage(initialRoute.page)
// Give an initial legacy deep link its owning context before it enters browser
// history, so Back cannot adopt the scene visited later.
if (!initialRoute.explicitScene && initialScene) attachSceneContext(initialRoute, initialScene.id)
const route = ref(initialRoute)
const navigationScopeVersion = ref(0)
const navigationContext = ref(readScenarioContext(initialRoute.slug))
const refreshNavigationScope = () => { navigationScopeVersion.value++; navigationContext.value = readScenarioContext(route.value.slug) }
function scopedHref(page: string) {
  void navigationScopeVersion.value
  return scenarioContextHref(route.value.slug, page)
}
const session = ref<Session | null>(null)
const initializing = ref(true)
const initialError = ref('')
const actionError = ref('')
const busy = ref(false)
const username = ref('')
const password = ref('')
const mainContent = ref<HTMLElement | null>(null)
const menuButton = ref<HTMLButtonElement | null>(null)
const menuOpen = ref(false)
const activeScenario = computed(() => {
  if (route.value.page === 'scenarios') return scenarios.find(item => item.id === route.value.recordId)
  return scenarios.find(item => item.id === route.value.sceneId && scenarioOwnsPage(item, route.value.page))
    || primaryScenarioForPage(route.value.page)
})
const activeTask = computed(() => route.value.page === 'scenarios' ? activeScenario.value?.tasks.find(item => item.id === route.value.contextId) : undefined)
const activePage = computed(() => route.value.page === 'overview' ? '概览' : route.value.page === 'scenarios'
  ? activeTask.value?.label || activeScenario.value?.label || '场景检查'
  : pages.find(item => item.id === route.value.page)?.label || '概览')
const navigationDialog = ref<HTMLDialogElement | null>(null)
const navigationInput = ref<HTMLInputElement | null>(null)
const navigationQuery = ref('')
const navigationMatches = computed(() => {
  const query = navigationQuery.value.trim().toLocaleLowerCase()
  const entries = query ? [...scenarioNavigation, ...toolNavigation, directoryNavigation] : [toolNavigation[0], ...scenarioNavigation.filter(item => item.group === '场景检查'), directoryNavigation]
  return entries.filter(item => `${item.label} ${item.group} ${item.path} ${item.keywords}`.toLocaleLowerCase().includes(query))
})
const activeGroup = computed(() => route.value.page === 'scenarios' ? activeTask.value ? activeScenario.value?.label : '场景检查'
  : route.value.page === 'tools' ? '工作区'
  : activeScenario.value?.label || toolGroups.find(group => group.items.some(item => item.id === route.value.page))?.label || '工作区')
const isProfessionalPage = computed(() => route.value.page === 'tools' || configurationToolGroups.some(group => group.items.some(item => item.id === route.value.page)))
const scenarioReturn = ref<{ slug: string; path: string; label: string; sceneId: string } | null>(null)

async function openNavigation() {
  navigationQuery.value = ''
  navigationDialog.value?.showModal()
  await nextTick()
  navigationInput.value?.focus()
}
function closeNavigation() { navigationDialog.value?.close() }
function navigationShortcut(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k' && workspace.value) {
    event.preventDefault()
    void openNavigation()
  }
}

function closeMenu() {
  if (!menuOpen.value) return
  menuOpen.value = false
  menuButton.value?.focus()
}
const workspace = computed(() => session.value?.workspaces.find(item => item.slug === route.value.slug))

function syncRoute() {
  const previous = route.value
  const previousScene = activeScenario.value
  const next = readRoute()
  const requestedScene = next.explicitScene
  // Existing business links keep their paths. Preserve scene context across
  // list/detail links so refresh and browser history retain the same menu.
  if (!next.explicitScene && previous.slug === next.slug && previousScene && scenarioOwnsPage(previousScene, next.page)) {
    attachSceneContext(next, previousScene.id)
  } else if (!next.explicitScene) {
    const primary = primaryScenarioForPage(next.page)
    if (primary) attachSceneContext(next, primary.id)
  }
  const scopeURL = new URL(window.location.href)
  const [scopePath = '', scopeQuery = ''] = scopeURL.hash.split('?')
  const scopeParameters = new URLSearchParams(scopeQuery)
  if (next.slug !== previous.slug) {
    for (const key of ['assetId', 'activityId', 'task']) scopeParameters.delete(key)
  } else {
    if (!scopeParameters.has('assetId') && !scopeParameters.has('activityId')) {
      const recordTarget = /^\d+$/.test(next.recordId) && ['activities', 'assessments', 'checks', 'classification', 'assets'].includes(next.page)
      const activityCheckTarget = next.page === 'checks' && next.recordId === 'new' && /^\d+$/.test(next.contextId)
      if (recordTarget || activityCheckTarget) {
        if (next.page === 'activities') scopeParameters.set('activityId', next.recordId)
        if (['classification', 'assets'].includes(next.page)) scopeParameters.set('assetId', next.recordId)
        if (activityCheckTarget) scopeParameters.set('activityId', next.contextId)
      } else {
        if (navigationContext.value.assetId) scopeParameters.set('assetId', navigationContext.value.assetId)
        if (navigationContext.value.activityId) scopeParameters.set('activityId', navigationContext.value.activityId)
      }
    }
    if (next.page !== 'scenarios' && next.sceneId === navigationContext.value.scene && navigationContext.value.task && !scopeParameters.has('task')) scopeParameters.set('task', navigationContext.value.task)
  }
  scopeURL.hash = scopePath + (scopeParameters.size ? '?' + scopeParameters : '')
  window.history.replaceState(window.history.state, '', scopeURL)
  navigationContext.value = readScenarioContext(next.slug)
  next.scopeAssetId = navigationContext.value.assetId
  next.scopeActivityId = navigationContext.value.activityId
  if (previous.page === 'scenarios' && previous.slug === next.slug && scenarios.some(item => item.id === previous.recordId)
    && toolPages.some(item => item.id === next.page) && next.page !== 'overview'
    && (!requestedScene || next.sceneId === previous.recordId)) {
    const scene = scenarios.find(item => item.id === previous.recordId)!
    scenarioReturn.value = { slug: previous.slug, sceneId: scene.id, path: `scenarios/${scene.id}${previous.contextId ? '/' + previous.contextId : ''}`, label: scene.tasks.find(item => item.id === previous.contextId)?.label || scene.label }
  } else if (next.slug !== previous.slug || ['overview', 'scenarios', 'tools'].includes(next.page)
    || requestedScene && scenarioReturn.value && next.sceneId !== scenarioReturn.value.sceneId) {
    scenarioReturn.value = null
  }
  route.value = next
  menuOpen.value = false
  actionError.value = ''
}

function routeToWorkspace(current: Session) {
  if (!route.value.slug && current.workspaces.length === 1) {
    window.location.hash = `#/${encodeURIComponent(current.workspaces[0].slug)}/overview`
  }
}

async function loadSession() {
  initializing.value = true
  initialError.value = ''
  try {
    const current = await request<Session>(sessionPath)
    session.value = current
    routeToWorkspace(current)
  } catch (error) {
    if (!(error instanceof ApiError && error.status === 401)) initialError.value = describeError(error)
  } finally {
    initializing.value = false
  }
}

async function signIn() {
  if (busy.value) return
  busy.value = true
  actionError.value = ''
  try {
    await request(sessionPath, {
      method: 'POST',
      body: JSON.stringify({ username: username.value.trim(), password: password.value }),
    })
    const current = await request<Session>(sessionPath)
    session.value = current
    routeToWorkspace(current)
    await nextTick()
    mainContent.value?.focus({ preventScroll: true })
  } catch (error) {
    actionError.value = error instanceof ApiError && error.status === 401
      ? '账号或密码不正确。'
      : describeError(error)
  } finally {
    password.value = ''
    busy.value = false
  }
}

async function signOut() {
  if (busy.value) return
  busy.value = true
  actionError.value = ''
  try {
    await request(sessionPath, { method: 'DELETE' })
    session.value = null
    scenarioReturn.value = null
    window.location.hash = '#/'
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) session.value = null
    else actionError.value = describeError(error)
  } finally {
    busy.value = false
  }
}

function sessionExpired() {
  scenarioReturn.value = null
  closeNavigation()
  session.value = null
  actionError.value = '登录已失效，请重新登录。'
}

function focusMain() {
  mainContent.value?.focus({ preventScroll: true })
}

watch(route, async () => {
  if (!session.value) return
  await nextTick()
  focusMain()
})

watch([workspace, route, session, initializing], () => {
  document.title = workspace.value
    ? `${activePage.value} · ${workspace.value.name} · 赤翎`
    : session.value ? '工作区 · 赤翎' : '登录 · 赤翎'
})

onMounted(() => {
  window.addEventListener('hashchange', syncRoute)
  window.addEventListener(scenarioScopeEvent, refreshNavigationScope)
  window.addEventListener('keydown', navigationShortcut)
  void loadSession()
})

onUnmounted(() => {
  window.removeEventListener('hashchange', syncRoute)
  window.removeEventListener(scenarioScopeEvent, refreshNavigationScope)
  window.removeEventListener('keydown', navigationShortcut)
})
</script>

<template>
  <a class="skip-link" href="#main-content" @click.prevent="focusMain">跳至主要内容</a>
  <div v-if="session && workspace" class="app-shell desktop-design">
    <aside class="sidebar" :class="{ 'sidebar-open': menuOpen }" @keydown.esc.stop="closeMenu">
      <Brand :href="scopedHref('overview')" />
      <button ref="menuButton" class="mobile-menu button button-secondary" type="button" aria-controls="workspace-navigation"
              :aria-expanded="menuOpen" @click="menuOpen = !menuOpen">
        <Icon :name="menuOpen ? 'close' : 'menu'" />菜单
      </button>
      <nav id="workspace-navigation" class="sidebar-nav" aria-label="工作区导航">
        <div class="nav-group">
          <a :href="scopedHref('overview')" :aria-current="route.page === 'overview' ? 'page' : undefined"><Icon name="overview" /><span>概览</span></a>
        </div>
        <div class="nav-group scenario-navigation">
          <span class="nav-label">按场景运行检查</span>
          <div v-for="scenario in scenarios" :key="scenario.id" class="scenario-nav-group">
            <a :href="scopedHref('scenarios/' + scenario.id)" :class="{ 'navigation-section-active': activeScenario?.id === scenario.id }"
               :aria-current="route.page === 'scenarios' && activeScenario?.id === scenario.id && !route.contextId ? 'page' : undefined">
              <Icon :name="scenario.icon" /><span>{{ scenario.label }}</span><Icon v-if="activeScenario?.id === scenario.id" class="scenario-nav-chevron" name="chevron" />
            </a>
            <div v-if="activeScenario?.id === scenario.id" class="scenario-subnavigation" :aria-label="scenario.label + '检查与发现'">
              <div class="scenario-subnavigation-group" role="group" :aria-labelledby="`${scenario.id}-results-heading`">
                <h3 :id="`${scenario.id}-results-heading`" class="scenario-subnavigation-label">结果与发现</h3>
                <div class="scenario-subnavigation-items">
                  <a v-for="view in scenario.views" :key="view.page" :href="scopedHref(scenarioEntryPath(scenario.id, view.page))"
                     :aria-current="route.page === view.page.split(/[/?]/)[0] && (!view.page.includes('tab=') || view.page.includes('tab='+route.capabilityTab)) ? 'page' : undefined">{{ view.label }}</a>
                </div>
              </div>
              <div class="scenario-subnavigation-group" role="group" :aria-labelledby="`${scenario.id}-guides-heading`">
                <h3 :id="`${scenario.id}-guides-heading`" class="scenario-subnavigation-label">专项技术检查</h3>
                <div class="scenario-subnavigation-items">
                  <a v-for="task in scenario.tasks" :key="task.id" :href="scopedHref('scenarios/' + scenario.id + '/' + task.id)"
                     :aria-current="route.page === 'scenarios' && route.contextId === task.id ? 'page' : undefined">{{ task.label }}</a>
                </div>
              </div>
            </div>
          </div>
        </div>
        <div class="nav-group navigation-support">
          <span class="nav-label">材料与工具</span>
          <a :href="scopedHref('reports')"><Icon name="reports" /><span>材料与报告</span></a>
          <a :href="scopedHref('tools')" :class="{ 'navigation-section-active': isProfessionalPage }" :aria-current="route.page === 'tools' ? 'page' : undefined"><Icon name="templates" /><span>专业工具与配置</span></a>
        </div>
      </nav>
      <div class="sidebar-footer">
        <a href="#/" class="sidebar-workspace"><Icon name="workspace" /><span><strong>{{ workspace.name }}</strong><small>{{ workspace.slug }} · 切换工作区</small></span><Icon name="chevron" /></a>
        <div class="sidebar-identity"><span class="account-avatar"><Icon name="user" /></span><span><strong>{{ session.username }}</strong><small>{{ roleLabels[workspace.role] || workspace.role }}</small></span></div>
      </div>
    </aside>
    <div class="workspace-shell">
      <header class="topbar">
        <div class="workspace-name">
          <a class="workspace-switcher" href="#/">工作空间</a>
          <Icon class="breadcrumb-divider" name="chevron" />
          <span class="workspace-crumb">{{ workspace.name }}</span>
          <span class="breadcrumb-divider" aria-hidden="true">/</span>
          <span class="page-crumb">{{ activeGroup }}</span><span class="breadcrumb-divider" aria-hidden="true">/</span><strong class="page-crumb-current" :title="activePage">{{ activePage }}</strong>
        </div>
        <button type="button" class="navigation-trigger" @click="openNavigation"><Icon name="search" /><span>查找场景或功能</span><kbd>⌘ / Ctrl K</kbd></button>
        <div class="topbar-account">
          <span class="account-avatar"><Icon name="user" /></span>
          <div class="account-name">
            <strong>{{ session.username }}</strong><small>{{ roleLabels[workspace.role] || workspace.role }}</small>
          </div>
          <button class="icon-button" type="button" aria-label="退出登录" title="退出登录"
                  :disabled="busy" @click="signOut"><Icon name="logout" /></button>
        </div>
      </header>
      <dialog ref="navigationDialog" class="navigation-dialog" aria-labelledby="navigation-dialog-title" @click="event => event.target === navigationDialog && closeNavigation()">
        <div class="navigation-dialog-heading"><h2 id="navigation-dialog-title">查找场景或功能</h2><button class="icon-button" type="button" aria-label="关闭功能查找" @click="closeNavigation"><Icon name="close" /></button></div>
        <label class="visually-hidden" for="navigation-query">场景或功能名称</label>
        <input id="navigation-query" ref="navigationInput" v-model="navigationQuery" placeholder="如分类分级、出境、合规审计、API" autocomplete="off">
        <nav aria-label="功能查找结果"><a v-for="item in navigationMatches" :key="item.path" :href="scopedHref(item.path)" @click="closeNavigation"><Icon :name="item.icon" /><span>{{ item.label }}<small class="navigation-result-group">{{ item.group }}</small></span><Icon name="arrow" /></a><p v-if="!navigationMatches.length" class="empty-state">没有匹配的场景或功能，请换个关键词。</p></nav>
      </dialog>
      <main id="main-content" ref="mainContent" class="main-content" tabindex="-1">
        <p v-if="actionError" class="message message-error page-feedback" role="alert">{{ actionError }}</p>
        <a v-if="scenarioReturn && scenarioReturn.slug === workspace.slug" class="scenario-return" :href="scopedHref(scenarioReturn.path)"><Icon name="back" />返回 {{ scenarioReturn.label }}</a>
        <ScenarioView v-if="route.page === 'scenarios'" :key="`${workspace.slug}/scenarios/${route.recordId}/${route.contextId}`" :workspace="workspace" :scenario-id="route.recordId" :task-id="route.contextId" @session-expired="sessionExpired" />
        <ToolsView v-else-if="route.page === 'tools'" :key="workspace.slug" :workspace="workspace" />
        <CapabilityToolsView v-else-if="route.page === 'capabilities'" :key="`${workspace.slug}/${route.scopeAssetId}/${route.scopeActivityId}`" :workspace="workspace" @session-expired="sessionExpired" />
        <template v-else-if="route.page === 'activities'">
          <ActivityCreate v-if="route.recordId === 'new'" :key="workspace.slug" :workspace="workspace"
                          @session-expired="sessionExpired" />
          <ActivityDetail v-else-if="route.recordId" :key="`${workspace.slug}/${route.recordId}`"
                          :workspace="workspace" :activity-id="route.recordId" @session-expired="sessionExpired" />
          <ActivitiesView v-else :key="workspace.slug" :workspace="workspace" @session-expired="sessionExpired" />
        </template>
        <PostgresInspectionView v-else-if="route.page === 'assets' && route.recordId"
                                :key="`${workspace.slug}/assets/${route.recordId}`" :workspace="workspace" :asset-id="route.recordId"
                                @session-expired="sessionExpired" />
        <template v-else-if="route.page === 'checks'">
          <CheckCreate v-if="route.recordId === 'new'" :key="`${workspace.slug}/${route.contextId}/${route.scopeAssetId}/${route.scopeActivityId}`"
                       :workspace="workspace" :activity-context="route.contextId"
                       @session-expired="sessionExpired" />
          <CheckDetail v-else-if="route.recordId" :key="`${workspace.slug}/${route.recordId}`"
                       :workspace="workspace" :check-id="route.recordId" @session-expired="sessionExpired" />
          <ChecksView v-else :key="workspace.slug" :workspace="workspace" @session-expired="sessionExpired" />
        </template>
        <template v-else-if="route.page === 'templates'">
          <TemplateForm v-if="route.recordId === 'new' || route.recordId === 'revise'"
                        :key="`${workspace.slug}/${route.recordId}/${route.contextId}`" :workspace="workspace"
                        :source-id="route.contextId" :revise="route.recordId === 'revise'" @session-expired="sessionExpired" />
          <TemplatesView v-else :key="`${workspace.slug}/${route.recordId}`" :workspace="workspace"
                         :revision-id="route.recordId" @session-expired="sessionExpired" />
        </template>
        <template v-else-if="route.page === 'reports'">
          <ReportArchiveView v-if="route.recordId === 'archives' && !route.contextId" :key="workspace.slug"
                             :workspace="workspace" @session-expired="sessionExpired" />
          <ComplianceReportView v-else :key="`${workspace.slug}/${route.recordId}/${route.contextId}`" :workspace="workspace"
                                :activity-context="route.recordId === 'archives' ? undefined : route.recordId"
                                :archive-id="route.recordId === 'archives' ? route.contextId : undefined"
                                @session-expired="sessionExpired" />
        </template>
        <AutomationView v-else-if="route.page === 'automation'" :key="`${workspace.slug}/${route.recordId}/${route.contextId}`"
                        :workspace="workspace" :check-context="route.recordId === 'new' ? route.contextId : undefined"
                        @session-expired="sessionExpired" />
        <template v-else-if="route.page === 'api-security'">
          <ApiRuntimeView v-if="route.recordId === 'runtime'" :key="workspace.slug"
                          :workspace="workspace" @session-expired="sessionExpired" />
          <ApiSecurityView v-else :key="`${workspace.slug}/api-security/${route.recordId}`"
                           :workspace="workspace" :scan-id="route.recordId" @session-expired="sessionExpired" />
        </template>
        <ApiLifecycleView v-else-if="route.page === 'api-lifecycle'" :key="workspace.slug"
                          :workspace="workspace" @session-expired="sessionExpired" />
        <DataFlowsView v-else-if="route.page === 'data-flows'" :key="workspace.slug"
                       :workspace="workspace" @session-expired="sessionExpired" />
        <RuntimePoliciesView v-else-if="route.page === 'runtime-policies'" :key="workspace.slug"
                             :workspace="workspace" @session-expired="sessionExpired" />
        <DiscoveryView v-else-if="route.page === 'discovery'" :key="workspace.slug"
                       :workspace="workspace" @session-expired="sessionExpired" />
        <AssetCatalogView v-else-if="route.page === 'asset-catalog'" :key="workspace.slug"
                          :workspace="workspace" @session-expired="sessionExpired" />
        <SecurityOperationsView v-else-if="route.page === 'operations'" :key="`${workspace.slug}/${route.recordId}`"
                                :workspace="workspace" :control-id="route.recordId" @session-expired="sessionExpired" />
        <AssessmentsView v-else-if="route.page === 'assessments'" :key="`${workspace.slug}/${route.recordId}`"
                         :workspace="workspace" :assessment-id="route.recordId" @session-expired="sessionExpired" />
        <ApiValidationView v-else-if="route.page === 'validation'" :key="`${workspace.slug}/${route.recordId}`"
                           :workspace="workspace" :plan-id="route.recordId" @session-expired="sessionExpired" />
        <template v-else-if="route.page === 'classification'">
          <ClassificationStandardsView v-if="route.recordId === 'standards'" :key="workspace.slug"
                                       :workspace="workspace" @session-expired="sessionExpired" />
          <ClassificationView v-else :key="`${workspace.slug}/classification/${route.recordId}`"
                              :workspace="workspace" :asset-id="route.recordId" @session-expired="sessionExpired" />
        </template>
        <AuditView v-else-if="route.page === 'audit'" :key="`${workspace.slug}/audit/${route.recordId}`" :workspace="workspace"
                   :tab="route.recordId === 'actions' ? 'actions' : 'runtime'" @session-expired="sessionExpired" />
        <WorkspaceView v-else :key="`${workspace.slug}/${route.page}`" :workspace="workspace" :page="route.page"
                       @session-expired="sessionExpired" />
      </main>
    </div>
  </div>

  <template v-else>
    <header v-if="session || initializing || initialError" class="public-header">
      <Brand />
      <button v-if="session" type="button" class="button button-secondary" :disabled="busy" @click="signOut">
        <Icon name="logout" />退出登录
      </button>
    </header>
    <main id="main-content" ref="mainContent" class="public-main" :class="{ 'public-main-login': !session && !initializing && !initialError }" tabindex="-1">
      <p v-if="initializing" class="message" role="status">正在连接工作台…</p>
      <section v-else-if="initialError" class="panel empty-state">
        <h1>暂时无法打开工作台</h1>
        <p role="alert">{{ initialError }}</p>
        <button type="button" class="button button-secondary" @click="loadSession">重试</button>
      </section>
      <template v-else-if="session">
        <header class="page-header">
          <div>
            <p class="eyebrow">治理工作区</p>
            <h1>选择工作区</h1>
            <p>进入已授权的工作区，查看资产和数据接入记录。</p>
          </div>
        </header>
        <p v-if="actionError" class="message message-error page-feedback" role="alert">{{ actionError }}</p>
        <p v-else-if="route.slug" class="message message-warning page-feedback" role="alert">此链接中的工作区不可访问。</p>
        <div class="panel workspace-list">
          <a v-for="access in session.workspaces" :key="access.slug" :href="`#/${encodeURIComponent(access.slug)}/overview`" class="workspace-row">
            <span class="workspace-symbol"><Icon name="workspace" /></span>
            <span class="workspace-row-name"><strong>{{ access.name }}</strong><small>{{ access.slug }}</small></span>
            <span class="badge">{{ roleLabels[access.role] || access.role }}</span><Icon name="arrow" />
          </a>
          <div v-if="!session.workspaces.length" class="empty-state">
            <strong>暂无可访问的工作区</strong>
            <p>请联系管理员，将你的账号添加到工作区。</p>
          </div>
        </div>
      </template>
      <div v-else class="login-layout">
        <section class="login-intro" aria-labelledby="login-intro-title">
          <Brand />
          <div class="login-proposition"><h1 id="login-intro-title">数据关系可追溯<br>检查证据可核验</h1><p>从来源观察到整改复测，保留每一步的范围与依据。</p></div>
          <img class="login-feather" :src="phoenixFeather" width="1024" height="1024" alt="" decoding="async">
          <p class="login-scope">授权工作区 · 数据资产 · 证据与复核</p>
        </section>
        <div class="login-form-region"><section class="panel login-panel" aria-labelledby="login-title">
          <h2 id="login-title">登录赤翎</h2>
          <p class="login-description">使用管理员分配的工作区账号。</p>
          <p v-if="actionError" class="message message-error page-feedback" role="alert">{{ actionError }}</p>
          <form @submit.prevent="signIn">
            <div class="field">
              <label for="login-username">账号</label>
              <input id="login-username" v-model="username" name="username" autocomplete="username" required>
            </div>
            <div class="field">
              <label for="login-password">密码</label>
              <input id="login-password" v-model="password" name="password" type="password" autocomplete="current-password" required>
            </div>
            <button type="submit" class="button button-primary" :disabled="busy">
              {{ busy ? '登录中…' : '登录' }} <Icon name="arrow" />
            </button>
          </form>
          <p class="login-access-note">如需工作区访问权限，请联系管理员。</p>
        </section></div>
      </div>
    </main>
  </template>
</template>
