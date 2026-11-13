export interface BrowserAnnotationCandidate {index:number;scanId:number;method:string;route:string;field:string;location:string;match:'exact_name';status:'candidate'}
export interface BrowserAnnotationField { name:string; label:string; elementKind:string; locator:string; category:string; targetField:string; note:string;apiCandidate?:BrowserAnnotationCandidate }
export interface BrowserAnnotationInput { assetId:number; page:{origin:string;path:string;title:string}; fields:BrowserAnnotationField[] }
export interface BrowserAnnotation extends BrowserAnnotationInput { id:number;assetName:string;actor:string;createdAt:string;snapshotDigest:string;replayed?:boolean }
export function parseBrowserAnnotation(value:unknown):Omit<BrowserAnnotationInput,'assetId'> {
  if(!value||typeof value!=='object')throw new Error('标签文件格式无效。')
  const input=value as Record<string,unknown>, page=input.page as Record<string,unknown>
  if(!page||typeof page.origin!=='string'||typeof page.path!=='string'||!Array.isArray(input.fields)||input.fields.length<1||input.fields.length>100)throw new Error('请选择包含1至100个字段的标签文件。')
  const text=(value:unknown,max:number)=>{if(value==null)return '';if(typeof value!=='string'||value.length>max||/[\u0000-\u001f\u007f]/.test(value))throw new Error('标签含有无效文本或长度超限。');return value}
  const origin=new URL(page.origin)
  if(!['http:','https:'].includes(origin.protocol)||origin.username||origin.password||origin.search||origin.hash||!['','/'].includes(origin.pathname)||!page.path.startsWith('/')||/[?#]/.test(page.path))throw new Error('页面标识须为不含查询参数和片段的HTTP或HTTPS来源与路径。')
  const allowed=new Set(['input','select','textarea','label','th','columnheader'])
  return {page:{origin:origin.origin,path:text(page.path,400),title:text(page.title,120)},fields:input.fields.map(raw=>{
    if(!raw||typeof raw!=='object')throw new Error('字段格式无效。')
    const f=raw as Record<string,unknown>
    // Reject data-bearing extensions to the structural-label-only format.
    if(Object.keys(f).some(key=>!['name','label','elementKind','locator','category','targetField','note'].includes(key)))throw new Error('字段文件含有不支持的数据项。只允许字段标识，不允许输入值或页面正文。')
    const result={name:text(f.name,120),label:text(f.label,120),elementKind:text(f.elementKind,20),locator:text(f.locator,200),category:text(f.category,80),targetField:text(f.targetField,160),note:text(f.note,240)}
    if((!result.name&&!result.label)||!allowed.has(result.elementKind))throw new Error('字段名称或元素类型无效。')
    return result
  })}
}
