import { collectFromActiveTab, platformImportUrl } from './collect.js'
const el = id => document.getElementById(id)
let payload
el('authorized').addEventListener('change', () => { el('collect').disabled = !el('authorized').checked })
el('collect').addEventListener('click', async () => {
  if (!el('authorized').checked) return
  el('collect').disabled = true
  el('status').textContent = '正在读取可见字段标识…'
  el('preview').hidden = true
  payload = undefined
  try {
    payload = await collectFromActiveTab(chrome)
    el('scope').textContent = payload.page.origin + payload.page.path
    el('fields').replaceChildren()
    payload.fields.forEach((field, index) => {
      const label = document.createElement('label')
      const checkbox = document.createElement('input'); checkbox.type = 'checkbox'; checkbox.checked = true; checkbox.dataset.index = String(index)
      const text = document.createElement('span'); text.textContent = field.label || field.name
      const detail = document.createElement('small'); detail.textContent = field.name || field.elementKind
      text.append(detail); label.append(checkbox, text); el('fields').append(label)
    })
    el('preview').hidden = !payload.fields.length
    el('status').textContent = payload.fields.length ? `识别到${payload.fields.length}项。请取消不应传递的标识。` : '没有可见字段标签；可先打开业务表单或弹窗。'
  } catch (error) { el('status').textContent = error.message || '当前页读取失败。' }
  finally { el('collect').disabled = !el('authorized').checked }
})
el('send').addEventListener('click', async () => {
  if (!payload) return
  try {
    const selected = [...el('fields').querySelectorAll('input:checked')].map(input => payload.fields[Number(input.dataset.index)])
    if (!selected.length) throw new Error('请选择至少一个可传递的字段标签。')
    const url = platformImportUrl(el('platform').value, { ...payload, fields: selected })
    await chrome.tabs.create({ url })
    payload = undefined; el('fields').replaceChildren(); el('preview').hidden = true
    el('status').textContent = '已打开平台预览，扩展中的临时数据已清除。'
  } catch (error) { el('status').textContent = error.message || '无法打开平台预览。' }
})
