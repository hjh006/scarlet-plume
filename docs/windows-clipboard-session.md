# Windows 当前用户会话剪贴板观察与条件清除

`internal/endpointnative/clipboard_session*.go` 为交互用户进程提供变化监听和有条件的**事后清除**。它不注入应用、不安装驱动，不拦截应用的 copy/paste，不证明其他进程在清除前没有读取或粘贴内容，也不清理 Windows 历史、云同步或其他应用保留的副本。清除后不保存、自动恢复原文。

本轮只有纯状态机/fake 测试和 Windows 三架构交叉编译；没有读取或清除真实剪贴板。Office、Edge/Chrome、记事本是首轮后续实机验收对象，尚无逐应用效果验收或兼容性等效结论。

## 源端 Go 契约

```go
CurrentClipboardSession() (ClipboardSessionIdentity, error)
WatchClipboardSession(ctx, shouldCapture func() bool,
    emit func(ClipboardSessionEvent) error) error
ClearClipboardIfCurrent(ctx, match ClipboardMatch) (ClipboardClearResult, error)
```

`CurrentClipboardSession` 查询当前主 token 的用户 SID、logon AuthenticationId 和 Windows session ID，以及窗口站/当前线程桌面是否可见并接收输入；不打开剪贴板。拒绝 SYSTEM、服务 SID、Session 0、线程 impersonation 和非活动/不可见桌面。

完成身份校验后，会在固定 `HKCU\Software\ScarletPlumeClipboardVolatile\<SID/AuthId/session 的摘要>` 初始化或读取 32 字节随机 `BootGeneration`。base 和 logon 子键均由 `RegCreateKeyExW(REG_OPTION_VOLATILE)` 创建，owner 与 protected DACL 限当前 SID/SYSTEM；未知归属、格式或权限不修复、不覆盖，直接拒绝。固定当前 logon 的 Local mutex 串行初始化，锁内二次读取防并发覆盖，已存在的有效 nonce 直接复用。没有 HKLM、GPO 或机器控制写入，也不保存正文。Plan/身份检查因此可能初始化**用户级易失元数据**，不能描述为“完全无写入”，但不读取剪贴板或更改其策略。

`Identity.LocalKey()` 包含 SID、AuthenticationId、session ID 和 BootGeneration，只供源端生成 HMAC。同一 logon 的 helper 重启保留引用；正常用户 hive 卸载或重启后易失键消失，即使 LUID/session 数字复用也产生新代际，旧批准不复活。剪贴板已打开时身份重检只读取现有代际：键缺失则拒绝清除，不等待初始化锁或创建键。当前用户可自行预植、修改自身注册表或终止自身进程，不能把这个机制称为同用户防篡改、自然人身份认证或内核强制 DLP。Windows 对已存在键不会按创建参数重新改变易失属性；人为预植同用户键不在此防篡改承诺内。

监听运行在锁定的 Windows 消息线程。只有成功注册 `AddClipboardFormatListener` 并取到元数据序号 baseline 后，才同步发一次 `Kind=listener_ready, Status=ready, Reason=listener_registered`；此事件无正文和清除资格。注册失败不发 ready。

启动不读取原有正文或枚举格式。相同 baseline 的通知忽略；`shouldCapture=false` 只更新通知序号，不调用 OpenClipboard/GetClipboardData。之后即使策略刚启用，也不读取策略启用前那一份内容，须再有新的变化通知。没有 sequence 定时轮询。

## 有界观察

正常通知 `Kind=content_changed`。仅在已批准的本地缓存允许时打开剪贴板，枚举最多 64 个数字格式 ID，并在存在 Unicode 表示时读取最多 16,384 个 UTF-16 单元。不查询格式名称、真实应用路径、窗口标题或账户显示名。

读取前后在同一次 OpenClipboard 持锁期间核对序号和 owner；读完立即 CloseClipboard，**然后**进入同步 `emit`。回调可做本地分类、metadata intent/fsync、缓存 policy TTL 重检和 Clear；网络投递应交给匿名 metadata outbox worker，不能持剪贴板锁等待 HTTP。回调返回或报错后，已读 Text 缓冲清零，Match 票据失效；报错退出监听。应用程序原本持有的副本和 Windows 管理的原内存不由本包证明已擦除。

事件字段：

| 字段 | 含义 |
| --- | --- |
| `Text` / `Formats` | 仅进程内的有界 Unicode UTF-8 副本和数字格式列表。 |
| `TextComplete` | Unicode 表示在预算内、终止正常且 UTF-16 有效。 |
| `FormatsComplete` | 格式枚举正常结束且未超 64 项。 |
| `Complete` | 稳定实例且只有文本表示被完整覆盖；Office HTML/其他混合格式使其为 false。完整空剪贴板也可为 true。 |
| `Stable` | 同次捕获前后的非零序号、owner 一致。 |
| `ClearEligible` | 同实例稳定、非零 owner、非空、context 有效；不是策略授权或敏感判断。 |
| `Match` | 序号/owner 和不可由网络反序列化构造的私有票据，仅可在本次 callback 中使用。 |
| `CaptureMillis` | 本次捕获耗时，不能解释为内容产生时间或端到端处置延迟保证。 |

部分 Unicode 的明确正命中可以由外层策略触发清除；应在完整 token 边界判断，避免把截断末端当作完整身份证号等标识。额外 HTML 表示不会抹掉已经确定的文本正命中；无命中且捕获不完整仍须 unknown。明确授权的 clear_all 可对非文本同实例进行条件清除，不要求读取图片等正文。

GetClipboardData 可能同步请求 owner 延迟渲染，native 调用不能被 Go context 强制取消。本实现不制造一个“超时已取消”的假象，同时让另一个 goroutine 继续读或持锁。渲染若令序号变化，则发 unstable，当前 Match 不清除；以后真实通知可重新捕获已经渲染且稳定的实例。通知可能合并或排队，不能证明每次复制都被及时观察。

## 条件清除与证据语义

`ClearClipboardIfCurrent` 必须在同步 callback 的同一监听线程调用。票据固定绑定原窗口、线程、SID/logon/session、序号、owner 和单调捕获时点，最长 5 秒；外层还必须独立检查当前批准策略和期限。伪造、修改公开 Match 字段、跨线程、回调后、过期、owner=0、sequence=0 都不会取得清除资格。

Clear 再次 OpenClipboard，在持锁状态重检**清前**序号、owner、当前身份和输入桌面；任一变化就不 EmptyClipboard。成功调用 EmptyClipboard 后仍在锁内枚举并确认格式为空，再关闭剪贴板。第一次尝试即消费票据，未知/失败不自动重复动作。

`ClipboardClearResult.CurrentSequence` 永远是清前比较值，`SequenceMatched` 记录这个比较；EmptyClipboard 导致的新序号不能被误当作 stale。`Attempted=true` 只表示调用过清除 API；只有当时空格式回读完整才给 `Status=cleared, ReadbackEmpty=true`。这个结果仅描述当下本地当前内容，不能叫“粘贴前阻断”。Close、Empty 或回读失败会保留 failed/unknown，不提升为预防成功。原文绝不进入清除回执。

固定事件状态包含 ready、captured、partial、empty、busy、unstable、unavailable；清除状态包含 cleared、stale、busy、session_unavailable、failed、unknown、unsupported。Reason 使用固定码，如 sequence_changed、owner_changed、stale_snapshot、session_changed、clipboard_busy、representations_incomplete、empty_failed、readback_unavailable、readback_not_empty、close_failed；不拼接系统原始异常、SID、路径或正文。

Identity、Match、Text、Formats 等原始字段均带 `json:"-"`；这只是减少误序列化，不替代调用者遵守“仅上传 HMAC 引用、分类与状态”的数据契约。不得将原对象交给可打印全部字段的日志器。

## 验证与待实机项

纯测试覆盖：ready 和策略禁用时零正文读、startup baseline、不持锁回调、seq/owner/session 在锁内竞争、延迟渲染序号变化、5 秒/线程/票据资格、混合 Office 格式/截断文本正信息保留、非文本 clear_all、清前序号证据、清后非空/回读失败、callback 失败 wipe 和隐私 JSON；还模拟多 helper 并发初始化、进程重启复用、hive 代际变化、旧票据拒绝，以及持剪贴板锁时不初始化元数据。Windows 专用测试只检查 token statistics ABI，不调用 token/desktop/clipboard API。

授权测试机就绪后，需分别在 Office、Edge/Chrome 和记事本用合成文本验证完整/混合/延迟格式、用户快速连续复制、锁屏/切换用户/断开的 RDP 会话、策略过期和 callback/outbox 失败；明确观察“其他应用是否已在清前粘贴”的竞态。当前不宣称这些实机项目已通过。

## Microsoft 官方依据

- [AddClipboardFormatListener](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-addclipboardformatlistener)、[Clipboard operations](https://learn.microsoft.com/en-us/windows/win32/dataxchg/clipboard-operations)：通知发生在内容变化后，延迟渲染可能同步阻塞，且 Open/Close 有明确持锁边界。
- [GetClipboardSequenceNumber](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getclipboardsequencenumber)：序号属于当前窗口站，权限不足返回零，延迟渲染会影响递增时点。
- [GetClipboardOwner](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getclipboardowner)、[EmptyClipboard](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-emptyclipboard)：owner 可能为空，清除会释放格式数据并改变 owner；不能据此推断此前无人读取。
- [TOKEN_STATISTICS](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-token_statistics)、[GetUserObjectInformationW](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getuserobjectinformationw)：主 token 的 AuthenticationId、窗口站可见标记以及桌面 UOI_IO 输入状态。
- [RegCreateKeyExW](https://learn.microsoft.com/en-us/windows/win32/api/winreg/nf-winreg-regcreatekeyexw)、[RegOpenCurrentUser](https://learn.microsoft.com/en-us/windows/win32/api/winreg/nf-winreg-regopencurrentuser)、[CreateMutexExW](https://learn.microsoft.com/en-us/windows/win32/api/synchapi/nf-synchapi-createmutexexw)：当前用户易失 key、避免错误的预定义 HKCU 缓存用户映射、受限会话 mutex 与并发初始化。
