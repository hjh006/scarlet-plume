<script setup lang="ts">
import { computed, defineAsyncComponent, h, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ApiError, describeError, request, roleLabels, sessionPath, type Session } from './api'
import Brand from './Brand.vue'
import FeatherMotif from './FeatherMotif.vue'
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

const pageGroups = [
  { label: '', items: [
    { id: 'overview', label: '概览' },
  ] },
  { label: '数据治理', items: [
    { id: 'assets', label: '数据资产' },
    { id: 'asset-catalog', label: '业务资产目录', icon: 'assets' },
    { id: 'classification', label: '分类分级' },
    { id: 'data-flows', label: '数据流转' },
    { id: 'activities', label: '处理活动' },
  ] },
  { label: '安全运营', items: [
    { id: 'operations', label: '运营与处置', icon: 'shield' },
    { id: 'api-security', label: 'API 安全' },
    { id: 'validation', label: 'API 主动验证', icon: 'shield' },
    { id: 'api-lifecycle', label: '接口生命周期' },
    { id: 'runtime-policies', label: '运行时检测策略', icon: 'templates' },
    { id: 'checks', label: '监管检查' },
    { id: 'assessments', label: '合规评估', icon: 'checks' },
    { id: 'reports', label: '合规报告' },
    { id: 'audit', label: '审计中心' },
  ] },
  { label: '配置与接入', items: [
    { id: 'discovery', label: '多源自动发现', icon: 'integrations' },
    { id: 'automation', label: '自动采集' },
    { id: 'templates', label: '检查模板' },
    { id: 'integrations', label: '集成与导入' },
  ] },
] as const
const pages = [...pageGroups[0].items, ...pageGroups[1].items, ...pageGroups[2].items, ...pageGroups[3].items]

function readRoute() {
  const [, rawSlug, requestedPage, recordId = '', contextId = ''] = window.location.hash.split('/')
  let slug = ''
  try {
    slug = decodeURIComponent(rawSlug || '')
  } catch {
    slug = ''
  }
  const page = pages.find(page => page.id === requestedPage)?.id || 'overview'
  return { slug, page, recordId, contextId }
}

const route = ref(readRoute())
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
const activePage = computed(() => pages.find(item => item.id === route.value.page)?.label ?? '概览')
const navigationDialog = ref<HTMLDialogElement | null>(null)
const navigationInput = ref<HTMLInputElement | null>(null)
const navigationQuery = ref('')
const navigationMatches = computed(() => pages.filter(item => `${item.label} ${item.id}`.toLowerCase().includes(navigationQuery.value.trim().toLowerCase())))
const activeGroup = computed(() => pageGroups.find(group => group.items.some(item => item.id === route.value.page))?.label || '数据治理')

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
  route.value = readRoute()
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
    window.location.hash = '#/'
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) session.value = null
    else actionError.value = describeError(error)
  } finally {
    busy.value = false
  }
}

function sessionExpired() {
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
  window.addEventListener('keydown', navigationShortcut)
  void loadSession()
})

onUnmounted(() => {
  window.removeEventListener('hashchange', syncRoute)
  window.removeEventListener('keydown', navigationShortcut)
})
</script>

<template>
  <a class="skip-link" href="#main-content" @click.prevent="focusMain">跳至主要内容</a>
  <div v-if="session && workspace" class="app-shell desktop-design">
    <aside class="sidebar" :class="{ 'sidebar-open': menuOpen }" @keydown.esc.stop="closeMenu">
      <Brand />
      <button ref="menuButton" class="mobile-menu button button-secondary" type="button" aria-controls="workspace-navigation"
              :aria-expanded="menuOpen" @click="menuOpen = !menuOpen">
        <Icon :name="menuOpen ? 'close' : 'menu'" />菜单
      </button>
      <nav id="workspace-navigation" class="sidebar-nav" aria-label="工作区导航">
        <div v-for="group in pageGroups" :key="group.label" class="nav-group">
          <span v-if="group.label" class="nav-label">{{ group.label }}</span>
          <a v-for="item in group.items" :key="item.id" :href="`#/${encodeURIComponent(workspace.slug)}/${item.id}`"
             :aria-current="item.id === route.page ? 'page' : undefined"
             @click="item.id === route.page && !route.recordId && closeMenu()">
            <Icon :name="'icon' in item ? item.icon : item.id" /><span>{{ item.label }}</span>
          </a>
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
          <span class="page-crumb">{{ activeGroup }}</span><span class="breadcrumb-divider" aria-hidden="true">/</span><strong class="page-crumb-current">{{ activePage }}</strong>
        </div>
        <button type="button" class="navigation-trigger" @click="openNavigation"><Icon name="search" /><span>查找功能或模块</span><kbd>⌘ / Ctrl K</kbd></button>
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
        <div class="navigation-dialog-heading"><h2 id="navigation-dialog-title">查找功能</h2><button class="icon-button" type="button" aria-label="关闭功能查找" @click="closeNavigation"><Icon name="close" /></button></div>
        <label class="visually-hidden" for="navigation-query">模块名称</label>
        <input id="navigation-query" ref="navigationInput" v-model="navigationQuery" placeholder="输入模块名称" autocomplete="off">
        <nav aria-label="功能查找结果"><a v-for="item in navigationMatches" :key="item.id" :href="`#/${encodeURIComponent(workspace.slug)}/${item.id}`" @click="closeNavigation"><Icon :name="'icon' in item ? item.icon : item.id" /><span>{{ item.label }}</span><Icon name="arrow" /></a><p v-if="!navigationMatches.length" class="empty-state">没有符合名称的功能，请调整查找内容。</p></nav>
      </dialog>
      <main id="main-content" ref="mainContent" class="main-content" tabindex="-1">
        <p v-if="actionError" class="message message-error page-feedback" role="alert">{{ actionError }}</p>
        <template v-if="route.page === 'activities'">
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
          <CheckCreate v-if="route.recordId === 'new'" :key="`${workspace.slug}/${route.contextId}`"
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
          <FeatherMotif />
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
