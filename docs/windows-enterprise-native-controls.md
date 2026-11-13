# Windows 企业终端固定原生控制器

本轮提供 `internal/endpointnativecontrol` 的固定 Windows 控制原语。租约审批、DPAPI 本地保存、服务安装、到期调度与平台回执由 Agent 外层处理。本包不安装驱动、不运行 shell、不接受注册表路径或任意命令，不重启系统、不刷新组策略，也不以配置写入推断功能阻断成功。

当前验证仅为合成状态机测试、协议集成与 Windows 交叉编译。用户暂无 Windows 测试机，**没有进行 Windows 实机配置、生效或恢复验收**。

| Executor / TargetScope | 唯一允许的动作 | 配置回读与实际覆盖 |
| --- | --- | --- |
| `windows_usb_removable_write` / `removable_storage_write` | HKLM 64 位视图的 `SOFTWARE\Policies\Microsoft\Windows\RemovableStorageDevices\{53f5630d-b6bf-11d0-94f2-00a0c91efb8b}`，仅 `Deny_Write=DWORD 1` | 可移动存储类写访问配置。不是全部 USB 总线设备控制，不禁止读取，不声称覆盖已打开句柄或立即生效。Microsoft 对可移动存储访问权限变更明确提示可能需重启；本包不会自动重启。 |
| `windows_print_queue_pause` / `configured_print_queues` | 对本地固定配置、实际被本机管理的队列调用 `SetPrinterW` 的 PAUSE；恢复只调用 RESUME | 回读本地队列 `PRINTER_STATUS_PAUSED`。不删除任务、不读 spool 正文、不改队列 ACL。已在打印的任务可能继续，暂停队列仍可接收新任务；不代表所有打印路径已阻断。 |
| `windows_rdp_clipboard_policy` / `rdp_clipboard_redirection` | HKLM 64 位视图 `SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services`，仅 `fDisableClip=DWORD 1` | 只回读 RDP 剪贴板重定向配置。可能需重新建立会话或重启并验证；不关闭会话、不清空剪贴板，不控制本地应用的复制粘贴。 |

三个控制器均只支持平台 `block_all` 模式；这里的“all”仅指表中的狭义目标范围。`Readback.Configured` 表示目标 DWORD 或暂停位的当前状态，不证明由本 Agent 设置，也不是功能验证。包返回的 `FunctionalVerified` 始终为 false；合成测试、注册表值、心跳或队列暂停位均不能升级为 `functional_verified`。

## 归属、权限和冲突

注册表控制保守限定为 Windows 10/11 客户端、build 19041 及以上的明确支持策略版本类型（Pro、Enterprise、Education、相应 N/工作站/教育及 IoT Enterprise 变体）。Home、Server、未知 SKU 或所需原生接口不可用时拒绝执行，不推断策略生效。

本机 AD 域加入、MDM 注册或管理状态查询失败时，注册表控制拒绝本地接管。这不是“检测到某条域策略冲突”的证明，而是尚未接入 RSOP/MDM 协同前的保守管理归属门禁。管理员需通过现有组织管理渠道配置此类设备。本包不尝试脱域、取消 MDM、修改策略优先级或取得注册表所有权。

任何已存在的目标 DWORD（包括显式 0 和 1）都拒绝接管；原先已经暂停的打印队列也拒绝接管。注册表需本来就具有查询、写值及必要的建子键权限，打印需队列管理权限；不会提升权限、扩大 ACL 或清除原有安全设置。打印只接受固定本地队列名，拒绝 UNC、远程连接和 `Xcv`/`Job` 特殊句柄语法。

固定 DWORD 的最终核验和修改在 KTM 注册表事务中完成；事务冲突、接口缺失或结果不能确认时拒绝，不降级为普通覆盖写。回滚只删除本租约原先不存在的目标 DWORD，保留可能创建的空 key，以免删除其他管理员新增的值或子键。

恢复在每次调用时重新核对目标引用、配置摘要、当前值/暂停位、队列配置身份，以及注册表 key 的最后变更 stamp。可观察到管理员更改时返回冲突，保留当前配置。打印 API 没有与外部管理员之间的状态比较事务；同值暂停、恢复后再暂停等不可观察的 ABA 意图不能被此接口证明或识别，需管理员协调该队列的独占控制范围。不能承诺排除拥有系统管理权限者的并发干预。

每个目标分别串行处理，某个打印提供程序阻塞不会占住其他目标的锁。Win32 打印接口自身可能同步阻塞，context 只能在原生调用之间检查；本包不提供硬实时 TTL 或即时功能恢复保证。

## Go 契约与本地恢复凭据

```go
New(targets []Target) (*Manager, error)
Expected(before Snapshot) (Snapshot, error) // 纯函数
TargetScope(executor string) string
Reason(err error) string // 无原值的业务说明

(*Manager).Capture(ctx, targetRef) (Snapshot, error)
(*Manager).Apply(ctx, before) (Readback, error)
(*Manager).Readback(ctx, targetRef) (Readback, error)
(*Manager).Restore(ctx, before, applied) (Readback, error)
```

`Target` 为源端固定配置，含 `Executor`、匿名 `TargetRef`、`ConfigDigest` 和仅打印使用的本地 `PrinterName`。最多 32 个队列和各一个 USB/RDP 控制目标；构造器不执行原生操作。引用和配置摘要由外层的匿名配置投影产生，必须为 64 位小写十六进制。不能上传 `Target` 中的原始队列名或完整本地快照。

`Snapshot` 可 JSON 序列化，含版本、executor、目标引用、配置摘要、phase、`StateDigest` 与原状态。`StateDigest` 的规范化计算排除 phase 和 `State.ChangeStamp`；所以 `Expected(before)` 的值摘要等于实际应用后的值摘要，恢复后又能等于 prior 值摘要。ChangeStamp 仅用于本地恢复冲突检测，不要求恢复旧时间戳。

外层服务必须遵守以下因果顺序：

1. Capture 原状态，Expected 计算 desired；先把二者、固定目标映射、租约期限和操作身份写入受保护的持久日志，完成平台 prepared 审计。
2. 调用 Apply；只有成功回读返回的 **phase=applied** 快照，才是可持久化的本次应用凭据。先将其可靠保存，再报告 configured/readback。多队列必须逐项记录成功项；没有跨队列事务。
3. TTL、撤销或卸载触发 Restore 时，仅传入保存的 captured 与 applied 快照。恢复前状态不一致返回 conflict，不覆盖当前配置。已恢复到 prior 值则无动作返回，支持进程恢复后的幂等重试。
4. 若进程在原生写入后、成功快照可靠保存前崩溃，仅有 intent/desired 的记录属于**不确定应用**。即使当前 DWORD/暂停位等于 desired，也不能证明是本进程写入；`Restore` 明确拒绝 desired 或普通 readback 快照，外层必须保留证据并转人工处理，不能认领其他管理员配置。原生写入无法与外层日志构成一个事务。

恢复值并不代表已验证权限或会话功能恢复，仍需执行对应的生效和合成行为验收。配置 digest/leaseRef 等回执是源端证据，不是防篡改的内核证明。

## 无设备操作的自动验收与待实机项

```sh
go test -race ./internal/endpointnativecontrol -count=1
go vet ./internal/endpointnativecontrol
GOOS=windows GOARCH=amd64 go vet ./internal/endpointnativecontrol
GOOS=windows GOARCH=amd64 go test -c ./internal/endpointnativecontrol -o /tmp/endpoint-native-controls.test.exe
```

纯测试使用内存状态，覆盖捕获/计划/应用/序列化后重建、精确值摘要恢复、重复恢复不重复写、已有配置拒绝、外部同值 stamp 修改和队列身份冲突、desired/readback 不能作为所有权凭据、未知目标/快照篡改/不完整回读、取消和目标间阻塞隔离。Windows 专用测试只检查 ABI、缓冲指针、固定 key 映射和 SKU 判定，不访问真实注册表或打印队列；交叉编译不会执行它们。

授权 Windows 测试环境提供后，仍需验证实际进程权限、工作组/AD/MDM 门禁、真实 DWORD 事务提交与回读、外部管理写入冲突、指定队列暂停与恢复、服务崩溃/重启和到期恢复。USB 使用合成文件验证类写限制及重新打开/重启边界；打印使用指定测试队列观察排队和当前任务边界；RDP 使用专门测试会话验证重定向限制且本地剪贴板仍正常。上述功能验收本轮均未执行。

## Microsoft 官方依据

- [Storage：RemovableDiskDenyWriteAccess](https://learn.microsoft.com/en-us/windows/client-management/mdm/policy-csp-storage#removablediskdenywriteaccess)；[RemovableStorage：AccessRights_RebootTime](https://learn.microsoft.com/en-us/windows/client-management/mdm/policy-csp-admx-removablestorage#accessrights_reboottime_2)：类写限制与重启生效边界。
- [可移动设备组策略排查](https://learn.microsoft.com/en-us/troubleshoot/windows-server/group-policy/removable-devices-group-policy-troubleshooting-guidance)：实际权限行为可能与注册表/GPO 回读不同，须核对管理来源和真实行为。
- [SetPrinter](https://learn.microsoft.com/en-us/windows/win32/printdocs/setprinter)、[GetPrinter](https://learn.microsoft.com/en-us/windows/win32/printdocs/getprinter)、[PRINTER_DEFAULTS](https://learn.microsoft.com/en-us/windows/win32/printdocs/printer-defaults)、[PRINTER_INFO_2](https://learn.microsoft.com/en-us/windows/win32/printdocs/printer-info-2)：队列管理权限、暂停语义与同步阻塞接口。
- [TerminalServer：TS_CLIENT_CLIPBOARD](https://learn.microsoft.com/en-us/windows/client-management/mdm/policy-csp-admx-terminalserver#ts_client_clipboard)：仅远程会话剪贴板重定向策略。
- [Registry key access rights](https://learn.microsoft.com/en-us/windows/win32/sysinfo/registry-key-security-and-access-rights)、[GetProductInfo](https://learn.microsoft.com/en-us/windows/win32/api/sysinfoapi/nf-sysinfoapi-getproductinfo)、[NetGetJoinInformation](https://learn.microsoft.com/en-us/windows/win32/api/lmjoin/nf-lmjoin-netgetjoininformation)、[IsDeviceRegisteredWithManagement](https://learn.microsoft.com/en-us/windows/win32/api/mdmregistration/nf-mdmregistration-isdeviceregisteredwithmanagement)：权限、版本与管理状态检查。
- [RegCreateKeyTransactedW](https://learn.microsoft.com/en-us/windows/win32/api/winreg/nf-winreg-regcreatekeytransactedw)、[CreateTransaction](https://learn.microsoft.com/en-us/windows/win32/api/ktmw32/nf-ktmw32-createtransaction)、[CommitTransaction](https://learn.microsoft.com/en-us/windows/win32/api/ktmw32/nf-ktmw32-committransaction)：固定注册表修改事务，不与外层 lease 日志混为一个原子提交。
