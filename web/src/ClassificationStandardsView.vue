<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, documentationUrl, request, workspacePath, type Workspace } from './api'
import { isReferencePublication, standardKindLabels, standardStatusLabels, standardsByIndustry, type ClassificationStandards } from './classification-standards'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const endpoint = workspacePath(props.workspace.slug) + '/classification/standards'
const owner = computed(() => props.workspace.role === 'owner')
const catalog = ref<ClassificationStandards>()
const industry = ref('')
const kind = ref('')
const chosen = ref('')
const selectedId = ref('')
const detailTab = ref('scope')
const search = ref('')
const loading = ref(true)
const saving = ref(false)
const loadError = ref('')
const saveError = ref('')
const notice = ref('')
const active = new AbortController()
const industries = computed(() => [...new Set(catalog.value?.items.map(standard => standard.industry) ?? [])])
const visible = computed(() => catalog.value?.items.filter(standard =>
  (!industry.value || standard.industry === industry.value) && (!kind.value || standard.kind === kind.value) && (!search.value.trim() || (standard.code+' '+standard.title).toLowerCase().includes(search.value.trim().toLowerCase())),
) ?? [])
const selectable = computed(() => catalog.value?.items.filter(standard => standard.status === 'effective') ?? [])
const selectableGroups = computed(() => standardsByIndustry(selectable.value))
const selected = computed(() => catalog.value?.items.find(item => item.id === selectedId.value) ?? visible.value[0] ?? null)
const current = computed(() => catalog.value?.items.find(standard => standard.id === catalog.value?.defaultStandardId))

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
    catalog.value = await request<ClassificationStandards>(endpoint, { signal: active.signal })
    chosen.value = selectable.value.some(standard => standard.id === catalog.value?.defaultStandardId)
      ? catalog.value.defaultStandardId
      : ''
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

async function saveDefault() {
  if (!owner.value || !catalog.value || saving.value || !selectable.value.some(standard => standard.id === chosen.value)) return
  saving.value = true
  saveError.value = ''
  notice.value = ''
  try {
    const updated = await request<{ defaultStandardId: string }>(endpoint + '/default', {
      method: 'PUT',
      body: JSON.stringify({ standardId: chosen.value }),
      signal: active.signal,
    })
    catalog.value = { ...catalog.value, defaultStandardId: updated.defaultStandardId }
    chosen.value = updated.defaultStandardId
    notice.value = '工作区默认标准已更新。'
  } catch (error) {
    saveError.value = errorText(error)
  } finally {
    saving.value = false
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
<div class="standards-view"><a class="back-link" :href="home+'/classification'"><Icon name="back"/>返回分类台账</a><header class="page-header"><div><p class="eyebrow">数据治理 / 标准与参照</p><h1>分类分级标准库</h1><p>按行业查阅已登记标准、规则和公开来源，采用版本固定到具体判定与证据。</p></div><button class="button button-secondary" type="button" :disabled="loading" @click="load">刷新目录</button></header><p v-if="loading" class="message" role="status">正在读取标准库…</p><div v-else-if="loadError" class="message message-error page-feedback"><p role="alert">{{loadError}}</p><button class="button button-secondary" type="button" @click="load">重新读取</button></div>
<div v-else-if="catalog" class="standards-workbench"><aside class="panel standards-industry"><div class="panel-heading"><h2>行业筛选</h2></div><nav aria-label="标准行业"><button type="button" :class="{active:!industry}" @click="industry='';selectedId=''">全部<span>{{catalog.items.length}}</span></button><button v-for="value in industries" :key="value" type="button" :class="{active:industry===value}" @click="industry=value;selectedId=''">{{value}}</button></nav><p class="field-hint">标准类型、目录资料、已配置规则分别列示。</p></aside>
<div class="section-stack"><section class="panel standards-catalog" aria-labelledby="standards-catalog-title"><div class="panel-heading"><div><h2 id="standards-catalog-title">已登记标准与参考依据</h2><p v-if="catalog.catalogAsOf">核验日期 {{catalog.catalogAsOf}}</p></div><span class="field-hint">{{visible.length}} 项</span></div><div class="filter-bar standards-filters"><label for="standard-kind-filter">标准类型</label><select id="standard-kind-filter" v-model="kind"><option value="">全部类型</option><option v-for="(label,value) in standardKindLabels" :key="value" :value="value">{{label}}</option></select><label class="visually-hidden" for="standard-search">搜索标准</label><input id="standard-search" v-model="search" placeholder="搜索标准编号或名称" maxlength="160"></div><div class="table-scroll standards-table-region" role="region" tabindex="0" aria-labelledby="standards-catalog-title"><table class="data-table standards-table"><thead><tr><th scope="col">标准 / 依据</th><th scope="col">状态与采用</th><th scope="col">规则范围</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="standard in visible" :key="standard.id" :class="{selected:selected?.id===standard.id}"><th scope="row"><button class="standard-select" type="button" @click="selectedId=standard.id">{{standard.title}}</button><span class="table-secondary mono">{{standard.code}}</span><span class="table-secondary">{{standard.industry}} · {{standardKindLabels[standard.kind]}}</span></th><td><span class="badge" :class="standard.status==='effective'?'status-verified':'status-draft'">{{standardStatusLabels[standard.status]}}</span><span v-if="standard.id===catalog.defaultStandardId" class="table-secondary"><span class="badge status-in_review">工作区默认</span></span><span v-if="isReferencePublication(standard)" class="table-secondary">发布资料，仅供参考</span></td><td>{{standard.rules?.length?standard.rules.length+' 条已配置规则':'条款规则待补充'}}<span class="table-secondary">{{standard.levels?.length||0}} 个登记等级</span></td><td><button class="button button-secondary" type="button" @click="selectedId=standard.id">查看</button></td></tr></tbody></table><p v-if="!visible.length" class="empty-state">没有符合筛选条件的标准。</p></div></section>
<section v-if="selected" class="panel standard-detail"><div class="panel-heading"><div><h2>标准详情</h2><p>{{selected.code}} · {{selected.title}}</p></div></div><div class="standards-tabs" role="group" aria-label="标准详情分类"><button type="button" :class="{active:detailTab==='scope'}" @click="detailTab='scope'">适用范围</button><button type="button" :class="{active:detailTab==='rules'}" @click="detailTab='rules'">等级与规则</button><button type="button" :class="{active:detailTab==='sources'}" @click="detailTab='sources'">公开来源</button></div><div class="standard-detail-body"><template v-if="detailTab==='scope'"><p>{{selected.scope}}</p><dl class="standard-detail-defs"><div><dt>发布机构</dt><dd>{{selected.authority}}</dd></div><div><dt>发布日期</dt><dd>{{selected.publishedOn||'未登记'}}</dd></div><div><dt>实施日期</dt><dd>{{selected.effectiveOn||'未登记'}}</dd></div></dl></template><template v-else-if="detailTab==='rules'"><p v-if="selected.levels?.length" class="standard-levels">登记等级：{{selected.levels.map(level=>level.label).join('、')}}</p><dl v-if="selected.rules?.length" class="standard-rule-list"><div v-for="rule in selected.rules" :key="rule.id"><dt>{{rule.clause}} · {{rule.title}}</dt><dd>{{rule.criterion}}</dd><dd v-if="rule.evidence" class="field-hint">取证要求：{{rule.evidence}}</dd></div></dl><p v-else class="field-hint">已收录目录信息，条款规则待补充。</p></template><template v-else><p class="field-hint">资料状态与规则覆盖按当前登记记录展示，目录收录不等于全文或所有条款已经实现。</p><a v-if="documentationUrl(selected.url)" class="table-link standard-source" :href="documentationUrl(selected.url)" target="_blank" rel="noopener noreferrer">查看官方来源<Icon name="external"/></a><p v-else class="field-hint">暂无可访问的已登记来源链接。</p></template></div></section></div>
<aside class="context-rail section-stack"><section class="panel" aria-labelledby="standard-default-title"><div class="panel-heading"><h2 id="standard-default-title">工作区默认标准</h2></div><form v-if="owner" class="standards-default" @submit.prevent="saveDefault"><div class="field"><label for="classification-default-standard">选择登记标准</label><select id="classification-default-standard" v-model="chosen" required :disabled="saving||!selectable.length"><option value="" disabled>请选择现行标准</option><optgroup v-for="[domain,standards] in selectableGroups" :key="domain" :label="domain"><option v-for="standard in standards" :key="standard.id" :value="standard.id">{{standard.code}} · {{standard.title}}</option></optgroup></select><p v-if="current&&current.status!=='effective'" class="field-hint">当前版本为{{current.code}}，请选择现行版本。</p></div><button type="submit" class="button button-primary" :disabled="saving||!chosen||chosen===catalog.defaultStandardId">{{saving?'正在保存…':'保存默认标准'}}</button><p v-if="notice" class="message message-success" role="status">{{notice}}</p><p v-if="saveError" class="message message-error" role="alert">{{saveError}}</p><p class="field-hint">默认标准用于预选，资产确认时仍需按实际适用条件选择。</p></form><p v-else class="standards-default">{{current?current.code+' · '+current.title:'尚未设置默认标准'}}<span class="field-hint">当前角色只能查看。</span></p></section><section class="panel"><div class="panel-heading"><h2>采用与判定</h2></div><p class="standard-detail-body field-hint">业务数据属性、企业内部保护等级与法定类别独立核验。标准名称、规则和版本来自当前标准库；未知条件继续取证。</p></section></aside></div></div>
</template>
<style scoped>
.standards-workbench{display:grid;grid-template-columns:190px minmax(0,1fr) 290px;gap:16px;align-items:start}.standards-industry nav{display:grid;gap:4px;padding:12px}.standards-industry nav button{display:flex;justify-content:space-between;padding:11px 12px;border:0;border-radius:6px;background:none;color:var(--ink);font:inherit;font-size:12px;text-align:left;cursor:pointer}.standards-industry nav button.active{background:#edf8fc;color:var(--accent)}.standards-industry>.field-hint{padding:0 18px 18px;font-size:11px}.standards-filters input{min-width:140px;max-width:240px;margin-left:auto}.standards-filters select{max-width:160px}.standards-table-region{max-height:570px;overflow:auto}.standards-table{font-size:12px}.standards-table :is(th,td){vertical-align:top;overflow-wrap:anywhere}.standards-table .table-secondary{font-size:10px}.standards-table tr.selected{background:#edf9fc}.standards-table tr.selected th{box-shadow:inset 3px 0 #1bb5cf}.standard-select{border:0;background:none;padding:0;text-align:left;font:inherit;font-weight:700;color:var(--ink);cursor:pointer;overflow-wrap:anywhere}.standard-detail-body{padding:18px;font-size:12px;line-height:1.8}.standard-detail-defs{display:grid;margin:14px 0 0}.standard-detail-defs>div{display:grid;grid-template-columns:80px minmax(0,1fr);gap:12px;padding:10px 0;border-top:1px solid var(--line)}.standard-detail-defs dt{color:var(--secondary)}.standard-detail-defs dd{margin:0}.standard-rule-list{margin:0}.standard-rule-list>div{padding:14px 0;border-bottom:1px solid var(--line)}.standard-rule-list dt{font-weight:700}.standard-rule-list dd{margin:7px 0 0}.standard-levels{margin-bottom:12px;color:var(--accent)}.standards-tabs{display:flex;gap:6px;padding:0 14px;border-bottom:1px solid var(--line)}.standards-tabs button{padding:12px 14px;border:0;border-bottom:2px solid transparent;background:none;color:var(--secondary);font:inherit;font-size:12px;cursor:pointer}.standards-tabs button.active{color:var(--accent);border-bottom-color:var(--accent)}.standards-default{display:grid;gap:16px;padding:18px;margin:0;font-size:12px}.standards-default .field{margin:0;min-width:0}.standards-default select{max-width:100%;min-width:0}.standards-default>.button{justify-self:start}.standard-source{display:inline-flex;gap:8px;align-items:center;margin-top:12px}.standard-source .icon{width:14px;height:14px}
@media(max-width:1400px){.standards-workbench{grid-template-columns:160px minmax(0,1fr) 260px}}
.standards-filters input{border:1px solid var(--input-line);border-radius:6px;padding:7px 10px;background:white;color:var(--ink)}
</style>
