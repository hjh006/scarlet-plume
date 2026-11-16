<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { aiFieldValue, aiMediaDraft, aiMediaMimeTypes, aiRecordDraft, aiRecordLabel, aiText, type AIEditorSection, type AIRecord } from './ai-security-editor'

const props = defineProps<{ section:AIEditorSection; records:AIRecord[]; disabled:boolean; singleton?:boolean; references?:Record<string,AIRecord[]> }>()
const emit = defineEmits<{ update:[records:AIRecord[]]; validation:[valid:boolean] }>()
const selected = ref(0)
const error = ref('')
const jsonDrafts = ref<Record<string,string>>({})
const record = computed(() => props.records[selected.value])
watch(() => props.section.key,() => { selected.value=0;error.value='';jsonDrafts.value={};emit('validation',true) })
watch(() => props.records.length,length => { if (selected.value>=length) selected.value=Math.max(0,length-1) })
function choose(index:number) { selected.value=index;error.value='';jsonDrafts.value={};emit('validation',true) }
function add() { const next=[...props.records,aiRecordDraft(props.section)];emit('update',next);selected.value=next.length-1;error.value='';jsonDrafts.value={} }
function remove() { emit('update',props.records.filter((_,index)=>index!==selected.value));selected.value=Math.max(0,selected.value-1);error.value='';jsonDrafts.value={} }
function validJSONDrafts() {
  try {
    for (const [key,value] of Object.entries(jsonDrafts.value)) {
      const field=props.section.fields.find(item=>item.key===key)
      if (field) aiFieldValue(field,value)
    }
    return true
  } catch { return false }
}
function update(key:string,value:string|boolean) {
  const field=props.section.fields.find(item=>item.key===key)
  if (!field || !record.value) return
  try {
    const next=props.records.slice()
    next[selected.value]={...record.value,[key]:aiFieldValue(field,value)}
    emit('update',next);error.value=validJSONDrafts()?'':'仍有未完成的 JSON 参数对象，请核对后再保存。';emit('validation',validJSONDrafts())
  } catch(cause) { error.value=cause instanceof Error ? `${field.label}：${cause.message}` : '请核对输入格式。';emit('validation',false) }
}
function jsonInput(key:string,event:Event) { const value=(event.target as HTMLTextAreaElement).value;jsonDrafts.value[key]=value;update(key,value) }
function setMedia(enabled:boolean) {
  if(!record.value)return
  const next=props.records.slice();next[selected.value]={...record.value,media:enabled?aiMediaDraft(record.value.media):null};emit('update',next)
}
function updateMedia(key:string,value:unknown) {
  if(!record.value)return
  const media=aiMediaDraft(record.value.media)
  const next=props.records.slice();next[selected.value]={...record.value,media:{...media,[key]:value}};emit('update',next)
}
function toggleMediaMime(type:string,checked:boolean) {
  const values=aiMediaDraft(record.value?.media).allowedMimeTypes as string[]
  updateMedia('allowedMimeTypes',checked?[...new Set([...values,type])]:values.filter(value=>value!==type))
}
function value(event:Event) { return (event.target as HTMLInputElement).value }
function selectedValues(event:Event) { return Array.from((event.target as HTMLSelectElement).selectedOptions).map(item=>item.value).join(',') }
function listValue(value:unknown) { return Array.isArray(value) ? value.map(String) : [] }
</script>

<template>
  <div>
    <div class="ai-security-heading"><div><h2>{{ section.label }}</h2><p>{{ section.description }}</p></div><button v-if="!disabled && !singleton" class="button button-secondary" type="button" @click="add">新增{{ section.label }}</button></div>
    <div v-if="records.length && !singleton" class="ai-security-records" :aria-label="section.label+'记录'"><button v-for="(item,index) in records" :key="index" type="button" :aria-pressed="selected===index" @click="choose(index)">{{ aiRecordLabel(item,index) }}</button></div>
    <div v-if="record" class="ai-security-body">
      <p v-if="error" class="message message-error" role="alert">{{ error }}</p>
      <fieldset class="ai-security-fields" :disabled="disabled">
        <template v-for="field in section.fields" :key="field.key">
          <label v-if="field.type==='checkbox'" class="ai-security-toggle"><input type="checkbox" :checked="record[field.key]===true" @change="update(field.key,($event.target as HTMLInputElement).checked)">{{ field.label }}<small v-if="field.help">{{ field.help }}</small></label>
          <div v-else-if="field.type==='media'" class="field-wide ai-security-note">
            <h3>{{ field.label }}</h3><label class="ai-security-toggle"><input type="checkbox" :checked="!!record.media" @change="setMedia(($event.target as HTMLInputElement).checked)">为此模型声明受控媒体输入<small>未声明时保持纯文本范围；检测能力需由实际节点与真实请求核实。</small></label>
            <div v-if="record.media" class="ai-security-fields">
              <div class="field field-wide"><span>明确允许的 MIME 类型</span><label v-for="mime in aiMediaMimeTypes" :key="mime" class="ai-security-toggle"><input type="checkbox" :checked="(aiMediaDraft(record.media).allowedMimeTypes as string[]).includes(mime)" @change="toggleMediaMime(mime,($event.target as HTMLInputElement).checked)">{{ mime }}</label></div>
              <label class="field">单个媒体最大字节数<input type="number" :value="aiMediaDraft(record.media).maxAssetBytes" required min="1" max="4194304" step="1" @input="updateMedia('maxAssetBytes',Number(value($event)))"></label>
              <label class="field">请求媒体总字节数<input type="number" :value="aiMediaDraft(record.media).maxTotalBytes" required min="1" max="8388608" step="1" @input="updateMedia('maxTotalBytes',Number(value($event)))"></label>
              <label class="field">最大像素数<input type="number" :value="aiMediaDraft(record.media).maxPixels" required min="1" max="16000000" step="1" @input="updateMedia('maxPixels',Number(value($event)))"></label>
              <label class="field">固定媒体检测版本<input :value="aiMediaDraft(record.media).inspectionVersion" readonly><small>scanner 是否可用及实际检测结果需独立核对。</small></label>
            </div><p class="table-secondary">{{ field.help }}</p>
          </div>
          <label v-else class="field" :class="{'field-wide':['textarea','json','lines'].includes(field.type||'')}">
            <span>{{ field.label }}<span v-if="field.required" aria-hidden="true"> *</span></span>
            <select v-if="field.type==='select'" :value="aiText(record[field.key]) || aiText(field.initial)" :required="field.required" @change="update(field.key,value($event))"><option v-if="!field.options?.some(option=>option.value==='')" value="">请选择</option><option v-for="option in field.options" :key="option.value" :value="option.value">{{ option.label }}</option></select>
            <textarea v-else-if="field.type==='json'" :value="jsonDrafts[field.key] ?? aiText(record[field.key])" rows="4" spellcheck="false" @input="jsonInput(field.key,$event)" @change="update(field.key,value($event))"></textarea>
            <textarea v-else-if="field.type==='lines'" :value="Array.isArray(record[field.key])?(record[field.key] as string[]).join('\n'):aiText(record[field.key])" rows="4" :required="field.required" maxlength="33000" spellcheck="false" @change="update(field.key,value($event))"></textarea>
            <textarea v-else-if="field.type==='textarea'" :value="aiText(record[field.key])" rows="3" :required="field.required" maxlength="32000" @input="update(field.key,value($event))"></textarea>
            <select v-else-if="field.reference" multiple size="4" @change="update(field.key,selectedValues($event))"><option v-for="(item,index) in references?.[field.reference] || []" :key="aiText(item.id)" :value="aiText(item.id)" :selected="listValue(record[field.key]).includes(aiText(item.id))">{{ aiRecordLabel(item,index) }} · {{ aiText(item.id) }}</option><option v-for="id in listValue(record[field.key]).filter(id=>!(references?.[field.reference!] || []).some(item=>aiText(item.id)===id))" :key="id" :value="id" :selected="true">{{ id }}（未找到对应配置）</option></select>
            <input v-else-if="field.type==='list'" type="text" :value="aiText(record[field.key])" :required="field.required" maxlength="4000" @change="update(field.key,value($event))">
            <input v-else :type="field.type==='number'?'number':field.type==='datetime'?'datetime-local':'text'" :value="aiText(record[field.key])" :required="field.required" :min="field.type==='number'?1:undefined" :max="field.max" :maxlength="field.maxLength || 4000" @input="update(field.key,value($event))">
            <small v-if="field.help">{{ field.help }}</small><small v-else-if="field.type==='lines'">每行一项，不拆分内容中的逗号。</small><small v-else-if="field.reference">可多选；应用及主体双方的授权范围仍由服务端逐项核对。</small><small v-else-if="field.type==='list'">多个值用逗号或换行分隔，空列表不表示允许所有对象。</small>
          </label>
        </template>
      </fieldset>
      <div v-if="!disabled && !singleton" class="ai-security-actions"><button class="button button-secondary" type="button" @click="remove">从待保存配置移除此记录</button><p>修改将在保存整个配置后生效。</p></div>
    </div>
    <div v-else class="ai-security-empty"><h3>尚未配置{{ section.label }}</h3><p>{{ section.description }}</p><button v-if="!disabled && !singleton" class="button button-secondary" type="button" @click="add">新增首条记录</button><p v-else>当前账号可以查看配置，请联系负责人或经办人完成接入。</p></div>
  </div>
</template>
