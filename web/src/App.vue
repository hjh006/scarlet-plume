<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ApiError, describeError, request, roleLabels, sessionPath, type Session } from './api'
import Brand from './Brand.vue'
import Icon from './Icon.vue'
import WorkspaceView from './WorkspaceView.vue'

const pages = [
  { id: 'overview', label: '概览' },
  { id: 'assets', label: '数据资产' },
  { id: 'integrations', label: '集成与导入' },
] as const

function readRoute() {
  const [, rawSlug, requestedPage] = window.location.hash.split('/')
  let slug = ''
  try {
    slug = decodeURIComponent(rawSlug || '')
  } catch {
    slug = ''
  }
  return { slug, page: pages.find(page => page.id === requestedPage)?.id || 'overview' }
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
const workspace = computed(() => session.value?.workspaces.find(item => item.slug === route.value.slug))

function syncRoute() {
  route.value = readRoute()
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
    mainContent.value?.focus()
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
  session.value = null
  actionError.value = '登录已失效，请重新登录。'
}

function focusMain() {
  mainContent.value?.focus()
}

watch(route, async () => {
  if (!session.value) return
  await nextTick()
  focusMain()
})

watch([workspace, route, session, initializing], () => {
  const page = pages.find(item => item.id === route.value.page)?.label
  document.title = workspace.value
    ? `${page} · ${workspace.value.name} · 赤翎`
    : session.value ? '工作区 · 赤翎' : '登录 · 赤翎'
})

onMounted(() => {
  window.addEventListener('hashchange', syncRoute)
  void loadSession()
})

onUnmounted(() => window.removeEventListener('hashchange', syncRoute))
</script>

<template>
  <a class="skip-link" href="#main-content" @click.prevent="focusMain">跳至主要内容</a>
  <div v-if="session && workspace" class="app-shell">
    <aside class="sidebar">
      <Brand />
      <nav class="sidebar-nav" aria-label="工作区导航">
        <span class="nav-label">治理工作区</span>
        <a v-for="item in pages" :key="item.id" :href="`#/${encodeURIComponent(workspace.slug)}/${item.id}`"
           :aria-current="item.id === route.page ? 'page' : undefined">
          <Icon :name="item.id" /><span>{{ item.label }}</span>
        </a>
      </nav>
      <div class="sidebar-footer">
        <a href="#/"><Icon name="workspace" />切换工作区</a>
      </div>
    </aside>
    <div class="workspace-shell">
      <header class="topbar">
        <div class="workspace-name"><Icon name="workspace" /><span>{{ workspace.name }}</span></div>
        <div class="topbar-account">
          <span class="account-avatar"><Icon name="user" /></span>
          <div class="account-name">
            <strong>{{ session.username }}</strong><small>{{ roleLabels[workspace.role] || workspace.role }}</small>
          </div>
          <button class="icon-button" type="button" aria-label="退出登录" title="退出登录"
                  :disabled="busy" @click="signOut"><Icon name="logout" /></button>
        </div>
      </header>
      <main id="main-content" ref="mainContent" class="main-content" tabindex="-1">
        <p v-if="actionError" class="message message-error page-feedback" role="alert">{{ actionError }}</p>
        <WorkspaceView :key="`${workspace.slug}/${route.page}`" :workspace="workspace" :page="route.page"
                       @session-expired="sessionExpired" />
      </main>
    </div>
  </div>

  <template v-else>
    <header class="public-header">
      <Brand />
      <button v-if="session" type="button" class="button button-secondary" :disabled="busy" @click="signOut">
        <Icon name="logout" />退出登录
      </button>
    </header>
    <main id="main-content" ref="mainContent" class="public-main" tabindex="-1">
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
          <span class="brand-mark"><Icon name="mark" /></span>
          <p class="eyebrow">数据安全治理工作台</p>
          <h1 id="login-intro-title">让数据来源可追溯</h1>
          <p>在授权的工作区登记资产，查看接入能力与元数据导入记录。</p>
        </section>
        <section class="panel login-panel" aria-labelledby="login-title">
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
        </section>
      </div>
    </main>
  </template>
</template>
