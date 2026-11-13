// This function is injected only on an explicit action. Keep it standalone:
// chrome.scripting serializes its body, without this module's lexical scope.
export function collectCurrentPageFields() {
  const clean = value => String(value || '').replace(/\s+/g, ' ').trim().slice(0, 120)
  const secret = /password|passwd|secret|token|authorization|api.?key|credential|密码|密钥|口令/i
  const fields = []
  const seen = new Set()
  for (const element of document.querySelectorAll('input,select,textarea,label,th,[role="columnheader"]')) {
    if (fields.length >= 100) break
    if (element.closest('[hidden],[aria-hidden="true"]') || element.getClientRects().length === 0) continue
    const tag = element.tagName.toLowerCase()
    const inputType = String(element.getAttribute('type') || '').toLowerCase()
    if (['password', 'hidden', 'submit', 'button', 'reset', 'file'].includes(inputType)) continue
    if(tag==='th'&&element.getAttribute('scope')==='row')continue
    const declaredName=element.getAttribute('name')||element.getAttribute('id')||(tag==='label'?element.getAttribute('for'):'')
    const name = clean(declaredName)
    // Read only label/heading text or declarative field attributes. Never read
    // input.value, textarea text, selected options, arbitrary body text or HTML.
    let label = ''
    if (['label', 'th'].includes(tag) || element.getAttribute('role') === 'columnheader') {
      // A nested control can carry values; reject that composite label rather
      // than traversing it or reading its textContent.
      if (element.querySelector('input,select,textarea,[contenteditable="true"]')) continue
      label = clean(element.textContent)
    } else {
      label = clean(element.getAttribute('aria-label') || element.getAttribute('placeholder'))
      if (!label && element.labels?.length) {
        const safe = Array.from(element.labels).find(el => !el.querySelector('input,select,textarea,[contenteditable="true"]'))
        if (safe) label = clean(safe.textContent)
      }
    }
    if ((!name && !label) || secret.test(name + ' ' + label + ' ' + element.getAttribute('autocomplete'))) continue
    const kind = element.getAttribute('role') === 'columnheader' ? 'columnheader' : tag
    const locator = `${kind}:visible-field(${fields.length + 1})`
    const key = name + '\u0000' + label
    if (seen.has(key)) continue
    seen.add(key)
    fields.push({ name, label, elementKind: kind, locator, category: '', targetField: '', note: '' })
  }
  return { version: 1, page: { origin: location.origin, path: location.pathname, title: '' }, fields }
}

export function platformImportUrl(platformUrl, payload) {
  const url = new URL(platformUrl)
  if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password || url.search) throw new Error('请粘贴平台工作区页面地址。')
  const [route] = url.hash.split('?')
  const slug = route?.split('/')[1]
  if (!slug) throw new Error('地址须包含已登录的平台工作区。')
  const bytes = new TextEncoder().encode(JSON.stringify(payload))
  if (bytes.length > 96 * 1024) throw new Error('待导入字段过多，请减少后重试。')
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  const encoded = btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  url.hash = `/${slug}/capabilities?tab=browser&annotation=${encoded}`
  return url.href
}

export async function collectFromActiveTab(chromeApi) {
  const [tab] = await chromeApi.tabs.query({ active: true, currentWindow: true })
  if (!tab?.id || !/^https?:\/\//.test(tab.url || '')) throw new Error('请打开已授权的 HTTP 或 HTTPS 业务页面。')
  const results = await chromeApi.scripting.executeScript({ target: { tabId: tab.id }, func: collectCurrentPageFields })
  const payload = results[0]?.result
  if (!payload || !Array.isArray(payload.fields)) throw new Error('当前页无法读取字段，请确认页面授权。')
  return payload
}
