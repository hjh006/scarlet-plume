# Windows 原生终端观察器

`backend/internal/endpointnative.Watch(ctx, emit)` 显式启动当前用户会话的观察。调用者同步将 `Observation` 的 `RawTarget`、`Sequence` 和 `Text` 转为匿名引用、分类及证据；不得将原结构写入 HTTP、日志或数据库。`emit` 返回后文本缓冲会清零，不应保留该缓冲。本包不联网、不更改策略、不读取打印正文、不删除或暂停任务，也不修改剪贴板。

| 通道 | 实际调用和证据 | 范围和限制 |
| --- | --- | --- |
| USB / 可移动卷 | 每 2 秒 `GetLogicalDriveStringsW` + `GetDriveTypeW`，比较 `DRIVE_REMOVABLE` 卷；`device_change`，目标为本地卷根，序号标记 arrived/removed | 启动只建基线；后续变化并非文件写入证据。读卡器等也可能属于可移动介质，固定类型 USB 硬盘、无盘符挂载、两次轮询间短暂变化不在完整覆盖内。无内容，`Complete=false`。 |
| 打印 | 每 3 秒 `EnumPrintersW(LOCAL\|CONNECTIONS, level 4)` + `OpenPrinterW` / `EnumJobsW(level 1)`；新增可见队列任务为 `job_observed` | 启动只建基线；最多 32 个已安装/已连接队列，各取前 64 个任务，单次缓冲不超过 1 MiB。仅使用队列名、任务 ID、提交时点；不取文档名、账户、机器名或 spool 正文。短任务可能漏报、权限和远程 RPC 可能失败或阻塞；无内容，`Complete=false`。 |
| 剪贴板 | 当前交互会话的隐藏消息窗，`AddClipboardFormatListener` / `WM_CLIPBOARDUPDATE` 后才打开剪贴板，读取 `CF_UNICODETEXT` | 启动不读已有内容、不使用 sequence 轮询。最多 16,384 个 UTF-16 单元；截断、无终止符、无效 UTF-16、锁竞争、非文本、其他表示格式或格式枚举不完整时为 `Complete=false`。只读文本表示；不读取图片、附件内容，不推断复制来源、粘贴目标或用户身份。 |

`Complete=true` 仅说明本次有界 Unicode 文本表示完整，不代表全部设备操作、所有应用或全部剪贴板格式均已覆盖。错误、权限和预算不足不会当作“没有敏感数据”；轮询通道发出 `observer_unavailable`，丢失窗口后重新建立基线。剪贴板窗口/监听注册失败时 `Watch` 返回不含原值的通用错误。非 Windows 返回 `ErrUnsupported`。

剪贴板观察器应由用户明确启动，运行在其交互登录会话中。普通 Windows 服务运行在 session 0，不能直接代表已登录用户桌面；本实现检查窗口站可见标记，拒绝用非交互窗口站冒充用户剪贴板覆盖。未来若采用常驻服务，需要单独的用户会话 helper 与受控 IPC；本轮没有修改登录项、计划任务或安装服务。

## 观察与预阻断

以上三个监听都是观察，不提供全系统内容预拦截。`WM_CLIPBOARDUPDATE` 在内容变化后到达，`EnumJobs` 枚举已经进入队列的任务；清空剪贴板、暂停/删除任务都不能证明内容从未被其他应用读取或已经打印。当前实现不做这些事。

受控 CLI/SDK 可在**自己的**复制、设置剪贴板、提交打印任务之前，复用 `pkg/datasafety` 的有界识别和策略判断。其证据应使用 `controlled_operations`，不能扩大为任意应用的全局防护。现有文本 guard 只接受 TXT/CSV/JSON/JSONL；其他格式或不完整识别保留 unknown。

Windows 的可移动存储拒写/拒绝全部访问策略能限制相应存储类，但它是管理员/组织管理的设备策略，不能从一次内容扫描推导出该策略已启用，且不是按文件敏感内容进行选择的拦截。本轮不写注册表、不启用策略、不改变用户设备访问。

## Microsoft 官方接口依据

- [AddClipboardFormatListener](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-addclipboardformatlistener) 与 [WM_CLIPBOARDUPDATE](https://learn.microsoft.com/en-us/windows/win32/dataxchg/wm-clipboardupdate)：通知剪贴板内容已经变化。
- [GetClipboardData](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getclipboarddata)、[OpenClipboard](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-openclipboard)、[EnumClipboardFormats](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-enumclipboardformats)：只在成功打开时复制所需数据，不能持有已关闭剪贴板的指针；格式枚举错误不等同正常结束。
- [GetDriveTypeW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-getdrivetypew)：区分介质类型，`DRIVE_REMOVABLE` 不等于全部 USB 总线设备。
- [EnumPrinters](https://learn.microsoft.com/en-us/windows/win32/printdocs/enumprinters)、[PRINTER_INFO_4](https://learn.microsoft.com/en-us/windows/win32/printdocs/printer-info-4)、[EnumJobs](https://learn.microsoft.com/en-us/windows/win32/printdocs/enumjobs)、[JOB_INFO_1](https://learn.microsoft.com/en-us/windows/win32/printdocs/job-info-1)：队列与已有任务元数据；[SetJob](https://learn.microsoft.com/en-us/windows/win32/printdocs/setjob) 处理已提交任务，不是提交前检查。
- [Interactive Services](https://learn.microsoft.com/en-us/windows/win32/services/interactive-services)：服务与交互用户会话的隔离及用户 helper 方案。
- [Storage / RemovableDiskDenyWriteAccess](https://learn.microsoft.com/en-us/windows/client-management/mdm/policy-csp-storage#removablediskdenywriteaccess) 与 [ADMX_RemovableStorage](https://learn.microsoft.com/en-us/windows/client-management/mdm/policy-csp-admx-removablestorage)：组织管理的存储类访问策略，部署前需核对 Windows 版本、版本类型与管理范围。

## 本轮验证范围

自动测试仅使用合成 UTF-16、卷快照与原生结构/缓冲样例。它们验证文本预算、非法/截断输入、卷变化语义、非 Windows 不支持，以及 Windows ABI 和缓冲指针边界；不会实际读取用户剪贴板、访问打印队列或修改设备。Windows 交叉编译不能替代真实 Windows 验收；原生监听的实际运行、用户会话、打印权限和设备变化仍需在获授权的 Windows 测试机上用合成数据验证。
