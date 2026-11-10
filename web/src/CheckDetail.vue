<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, documentationUrl, formatDate, request, workspacePath, type Workspace } from './api'
import { activityCheckHints, activityKindLabels } from './activity'
import { checkStatusLabels, evidenceKindLabels, eventLabels, type CheckDetail } from './workflow'
import CheckEvidenceForm from './CheckEvidenceForm.vue'
import Icon from './Icon.vue'
import TemplateCriteria from './TemplateCriteria.vue'

const props = defineProps<{ workspace: Workspace; checkId: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const apiUrl = workspacePath(props.workspace.slug) + '/checks/' + encodeURIComponent(props.checkId)
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const pageUrl = home + '/checks'
const record = ref<CheckDetail | null>(null)
const loading = ref(true)
const loadError = ref('')
const actionError = ref('')
const notice = ref('')
const actionPending = ref(false)
const actionConflict = ref(false)
const formVersion = ref(0)
const decision = ref<'verified' | 'changes' | ''>('')
const reviewNote = ref('')
const active = new AbortController()

const changes = computed(() => record.value?.status === 'needs_work'
  ? record.value.events.find(event => event.action === 'changes') : undefined)
const activityHint = computed(() => record.value?.activity ? activityCheckHints[record.value.activity.kind] : undefined)
const requirementUrl = computed(() => documentationUrl(record.value?.requirement.sourceUrl || ''))

function errorText(error: unknown) {
  if (active.signal.aborted) return ''
  if (error instanceof ApiError && error.status === 401) {
    emit('sessionExpired')
    return ''
  }
  return describeError(error)
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const updated = await request<CheckDetail>(apiUrl, { signal: active.signal })
    if (updated.reviewRevision !== record.value?.reviewRevision) {
      decision.value = ''
      reviewNote.value = ''
    }
    record.value = updated
    actionConflict.value = false
    actionError.value = ''
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

async function saveAction(action: 'submit' | 'review') {
  if (!record.value || actionPending.value || loading.value || actionConflict.value) return
  actionPending.value = true
  actionError.value = ''
  notice.value = ''
  try {
    const payload = action === 'submit' ? {} : {
      reviewRevision: record.value.reviewRevision, decision: decision.value, note: reviewNote.value.trim(),
    }
    await request(apiUrl + '/' + action, { method: 'POST', body: JSON.stringify(payload) })
    if (active.signal.aborted) return
    notice.value = action === 'submit' ? '已提交，等待独立复核。' : '已记录复核结论。'
    decision.value = ''
    reviewNote.value = ''
    await load()
  } catch (error) {
    actionConflict.value = error instanceof ApiError && error.status === 409
    actionError.value = errorText(error)
  } finally {
    actionPending.value = false
  }
}

async function evidenceSaved(message: string) {
  if (active.signal.aborted) return
  notice.value = message
  formVersion.value++
  await load()
}

onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <div class="check-record">
    <a class="back-link" :href="pageUrl"><Icon name="back" />返回监管检查</a>
    <p v-if="loading && !record" class="message" role="status">正在读取检查详情…</p>
    <div v-if="loadError" class="message message-error page-feedback">
      <p role="alert">{{ loadError }}</p>
      <button type="button" class="button button-secondary" @click="load">重新读取</button>
    </div>

    <template v-if="record">
      <header class="page-header">
        <div>
          <p class="eyebrow">检查记录 <span class="mono">#{{ record.id }}</span></p>
          <h1>{{ record.template?.item.title || record.requirement.title }}</h1>
          <p>{{ record.assetName }} · {{ record.ownerTeam }}</p>
        </div>
        <div class="page-actions">
          <a class="button button-secondary" :href="apiUrl + '/export'"><Icon name="download" />导出 JSON</a>
          <button v-if="record.canSubmit" type="button" class="button button-primary"
                  :disabled="actionPending || loading || actionConflict || Boolean(loadError)" @click="saveAction('submit')">提交复核</button>
        </div>
      </header>
      <p v-if="notice" class="message message-success page-feedback" role="status">{{ notice }}</p>
      <div v-if="actionError" class="message message-error page-feedback">
        <p role="alert">{{ actionError }}</p>
        <button v-if="actionConflict" type="button" class="button button-secondary" :disabled="loading" @click="load">刷新检查记录</button>
      </div>

      <section class="panel" aria-labelledby="check-summary-title">
        <div class="panel-heading">
          <div><h2 id="check-summary-title">检查信息</h2><p>提交后由具备权限且未参与经办的账号独立复核。</p></div>
          <span class="badge status" :class="'status-' + record.status">{{ checkStatusLabels[record.status] }}</span>
        </div>
        <dl class="detail-list">
          <div><dt>关联资产</dt><dd>{{ record.assetName }}</dd></div>
          <div v-if="record.djangoId"><dt>Django 原检查编号</dt><dd>#{{ record.djangoId }}</dd></div>
          <div v-if="record.activity">
            <dt>关联处理活动</dt>
            <dd>
              <a class="table-link" :href="home + '/activities/' + record.activity.id">{{ record.activity.name }}</a>
              <span class="table-secondary">{{ activityKindLabels[record.activity.kind] }}</span>
            </dd>
          </div>
          <div><dt>整改责任团队</dt><dd>{{ record.ownerTeam }}</dd></div>
          <div><dt>更新时间</dt><dd class="date-text"><time :datetime="record.updatedAt">{{ formatDate(record.updatedAt) }}</time></dd></div>
          <div>
            <dt>监管要求来源</dt>
            <dd>
              <a v-if="requirementUrl" class="source-link" :href="requirementUrl" target="_blank" rel="noopener noreferrer">
                {{ record.requirement.citation || record.requirement.code }} <Icon name="external" />
              </a>
              <span v-else>{{ record.requirement.citation || record.requirement.code }}</span>
            </dd>
          </div>
          <div v-if="record.requirement.summary" class="detail-wide"><dt>要求说明</dt><dd class="text-block">{{ record.requirement.summary }}</dd></div>
          <div v-if="record.feishuTargetDigest" class="detail-wide">
            <dt>飞书采集范围</dt>
            <dd class="text-block">核对指定协作者的文档直接授权。链接、继承和群组授权等访问路径需单独核验。</dd>
          </div>
          <div class="detail-wide"><dt>检查范围与标准</dt><dd class="text-block">{{ record.note }}</dd></div>
          <div v-if="record.template" class="detail-wide">
            <dt>检查模板</dt>
            <dd>
              <a class="table-link" :href="home + '/templates/' + record.template.revisionId">{{ record.template.title }}</a>
              <span class="table-secondary">版本 {{ record.template.version }} · {{ record.template.item.title }}</span>
              <TemplateCriteria :item="record.template.item" />
            </dd>
          </div>
        </dl>
      </section>

      <div class="detail-grid">
        <section class="panel" aria-labelledby="check-evidence-title">
          <div class="panel-heading">
            <div><h2 id="check-evidence-title">证据记录</h2><p>按来源标识和观察时间追溯核验依据。</p></div>
          </div>
          <div v-if="activityHint" class="activity-guidance">
            <strong>建议核对的证据</strong>
            <p>{{ activityHint.scope }}</p>
            <p v-if="changes">{{ activityHint.retest }}</p>
            <p>仅登记记录编号或脱敏摘要，原始业务数据和提示词留在源系统。</p>
          </div>
          <div v-if="changes" class="evidence-guidance">
            <strong>本轮整改要求</strong>
            <p class="text-block">{{ changes.note }}</p>
            <ol v-if="record.feishuTargetDigest" class="remediation-steps">
              <li>在飞书移除指定协作者的直接授权。</li>
              <li>手工登记「整改记录」，关联变更记录。</li>
              <li>重新采集并导入飞书权限清单。</li>
              <li>提交独立复核。</li>
            </ol>
            <p v-else-if="record.canAddEvidence && !record.canSubmit">登记整改后新取得的复测证据，再提交独立复核。</p>
          </div>
          <CheckEvidenceForm v-if="record.canAddEvidence && !loadError" :key="formVersion" :api-url="apiUrl"
                             :retest="record.status === 'needs_work'" :target-digest="record.feishuTargetDigest"
                             @saved="evidenceSaved" @session-expired="emit('sessionExpired')" />

          <article v-for="entry in record.evidence" :key="entry.id" class="evidence">
            <div class="evidence-heading"><h3>{{ entry.sourceSystem }}</h3><span class="badge">{{ evidenceKindLabels[entry.kind] }}</span></div>
            <p class="evidence-ref">记录标识 <span class="mono">{{ entry.sourceRecord }}</span></p>
            <p v-if="entry.feishu" class="field-hint">客户侧上传 · {{ entry.feishu.memberPresent ? '检出目标协作者的直接授权' : '未检出目标协作者的直接授权' }}</p>
            <p class="text-block">{{ entry.summary }}</p>
            <div class="evidence-meta">
              <span>观察于 <time :datetime="entry.observedAt">{{ formatDate(entry.observedAt) }}</time></span>
              <span>{{ entry.recordedBy }} 登记于 <time :datetime="entry.createdAt">{{ formatDate(entry.createdAt) }}</time></span>
            </div>
          </article>
          <div v-if="!record.evidence.length" class="empty-state">
            <strong>尚未登记证据</strong>
            <p>登记源系统记录的标识和脱敏摘要，以便复核时定位原始记录。</p>
          </div>
        </section>

        <section class="panel" aria-labelledby="check-events-title">
          <div class="panel-heading"><div><h2 id="check-events-title">操作历史</h2><p>查看登记、提交和复核记录。</p></div></div>
          <ol v-if="record.events.length" class="timeline">
            <li v-for="event in record.events" :key="event.id">
              <strong>{{ eventLabels[event.action] }}</strong>
              <div class="event-meta">{{ event.actor }} · <time :datetime="event.createdAt">{{ formatDate(event.createdAt) }}</time></div>
              <p v-if="event.note" class="text-block">{{ event.note }}</p>
            </li>
          </ol>
          <div v-else class="empty-state"><p>尚无操作记录。</p></div>
        </section>
      </div>

      <section v-if="record.canReview" class="panel review-panel" aria-labelledby="check-review-title">
        <div class="panel-heading">
          <div><h2 id="check-review-title">独立复核</h2><p>核对本次提交的证据，说明核验结果或待整改事项。</p></div>
        </div>
        <form class="evidence-form" @submit.prevent="saveAction('review')">
          <p v-if="record.feishuTargetDigest" class="field-hint page-feedback">
            客户侧清单由经办人上传。请对照飞书源端权限，并单独核验链接、继承和群组等访问路径。
          </p>
          <p v-if="!record.canVerify" class="message message-warning page-feedback">
            当前飞书清单仍检出目标协作者的直接授权。请记录整改要求。
          </p>
          <div class="field">
            <label for="check-review-decision">复核决定</label>
            <select id="check-review-decision" v-model="decision" required>
              <option value="" disabled>选择复核决定</option>
              <option value="changes">要求整改</option>
              <option v-if="record.canVerify" value="verified">复核通过</option>
            </select>
          </div>
          <div class="field">
            <label for="check-review-note">复核意见</label>
            <textarea id="check-review-note" v-model="reviewNote" maxlength="600" required></textarea>
          </div>
          <div class="form-actions">
            <button type="submit" class="button button-primary" :disabled="actionPending || loading || actionConflict || Boolean(loadError)">
              {{ actionPending ? '正在保存…' : '保存复核结果' }}
            </button>
          </div>
        </form>
      </section>
    </template>
  </div>
</template>
