# 羽毛 Logo 与前端精修（2026-11-12）

按用户最新要求，平台继续沿用已批准的蓝、青、紫渐变：侧栏、登录品牌区和关系图谱均保留这组色谱。朱红、金黄、乳白与少量黄绿只用于新的凤凰羽毛品牌素材；中途尝试的暖色平台主题已撤回。

## 素材

- `phoenix-feather-source.png`：原始透明 PNG，保留用户提供参考图的细密羽丝、半透明层次和金色羽轴。
- `../../../web/src/assets/phoenix-feather-mark.webp`：256px 导航与页头图标，约17KB。
- `../../../web/src/assets/phoenix-feather.webp`：1024px 登录页品牌插图，约166KB。
- `../../../web/src/assets/phoenix-feather-favicon.png`：64px 浏览器图标。

采用内置 ImageGen 编辑参考图；未使用 CLI 或其他模型。随后仅做 WebP 编码与尺寸转换，保留透明度。品牌组件图片使用空 alt，由旁边的“赤翎 / Scarlet Plume”文字提供链接名称。大图仅为品牌装饰，不表示业务节点、来源或真实统计。

配色参考为用户指定的[凤王官方图鉴](https://in.portal-pokemon.com/pokedex/0250/)，形态依据为用户明确选中的原创羽毛图。

## 实际提示词

> Use case: precise-object-edit, logo-brand. Edit target: the attached soft translucent phoenix feather image. This is the approved texture and silhouette for the Scarlet Plume enterprise brand; preserve its diagonal bottom-left stem to upper-right tip shape, organic wispy contour, exceptionally fine layered feather filaments, soft translucent dimensional body and slender luminous golden rachis. CHANGE ONLY its palette to an elegant Ho-Oh-inspired balance: saturated vermilion scarlet red and warm orange in the central vane (about 65%), luminous rich golden-yellow transition in the upper quarter (about 20%), creamy ivory translucent outer filaments (about 12%), just a tiny warm yellow-green glint at the very far tip (at most 3%). No large green area. Keep the overall warm scarlet identity, feathery/silky rather than metallic. Soft natural depth, bright gold axial light, exquisite premium detail, clean silhouette legible when reduced. Real transparent alpha background, feather centered with narrow breathing room around contour, no black background, no ground, no cast shadow, no frame, no text, no other objects, no birds or characters. Do NOT turn it into rigid plated metal, a wing, a jewel, a flame, a rainbow gradient or a flat vector. Preserve the approved organic feather form and detail while recoloring.

## 前端变更范围

保留19个现有模块、真实业务数据、深链接与后端权限。精修共享表格、表单、筛选器、按钮、关系图标签、侧栏、状态及详情栏：主要表格和输入提升至13px，原有小字号元数据提升至12px；修正窄桌面列宽和内部横滚，统一选中状态，并向选择控件补充 `aria-pressed`。运营趋势计数与日期分行，评估筛选控件底部对齐。

登录页采用新的羽毛品牌图，账号表单保持真实认证流程。字体沿用仓库本地 Grove Sans SC，未新增字体或前端运行依赖。打印仍采用现有A4与来源追溯规则。

验证材料保存在本地 `/Users/simon/.codex/visualizations/2026/11/12/scarlet-hooh-refinement`；其中 `preview` 是已撤回的暖色方案，`blue-preview` 是恢复蓝青紫后的主页面检查，后续修正和本地发布以对应最终记录为准。桌面检查范围为1600×1000、1366×900、1280×900；本轮未扩大到手机端或重新执行厂商业务闭环验收。
