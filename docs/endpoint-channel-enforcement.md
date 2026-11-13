# USB、打印与剪贴板接入

2026-11-13。本轮优先交付 Windows 执行器。平台策略、独立审批、设备绑定、回执及导出支持上述三通道；原生观察与显式受控动作分别记录。本机为 macOS，Windows 二进制交叉编译和合成协议回归不冒充 Windows 设备实测。

## 执行范围

| 通道 | Windows 原生观察 | 受控动作的检查点 | 已知覆盖边界 |
| --- | --- | --- | --- |
| USB | 可移动卷变化 | `CopyUSB` 写入已验证的可移动卷前 | 插拔不代表文件外发；`DRIVE_REMOVABLE` 不覆盖全部 USB 总线设备或固定型 USB 硬盘。其他应用直接写盘不经过此受控入口。 |
| 打印 | 已有队列任务的有界元数据轮询 | 受控文本调用 GDI `StartDocW` 之前 | 不读取第三方 spool 正文，不删除或暂停第三方任务；提交成功仅证明入队，不能证明纸张打印完成。短任务可能在轮询间隔内遗漏。 |
| 剪贴板 | 监听注册后的新变化，有界 Unicode 文本在源端分类 | `EmptyClipboard` / `SetClipboardData` 之前 | 普通应用的变化通知发生在写入后，不作为预阻断证据；只覆盖当前交互用户窗口站，不能代表其他会话或全部格式。 |

`controlled_operations` 只保护通过执行器/同一 SDK 的显式动作。`observation` 不执行阻断。当前不接受 `native_control` 声明为已支持，不把菜单或能力声明显示为全系统拦截已生效。原生观察 API、边界和官方依据见[原生观察说明](windows-native-endpoint-observation.md)。

## 平台工作流

1. 负责人在“运营与处置”登记独立的安全事件来源。每台终端使用独立来源令牌和固定客户侧 HMAC 设备引用；首次心跳绑定后不可用同一来源换设备/操作系统。
2. 执行器心跳报告编译具备的能力；界面同时列出最近心跳、来源是否有效、观察范围和执行范围。超过5分钟没有新心跳保持离线/未知，不能从最后一次配置推出当前仍在监测。
3. 在“数据外发与事件调查 → USB、打印与剪贴板策略”中选择实际设备/通道。策略支持监测、阻断指定敏感类别、阻断全部受控动作。策略含来源、设备、范围、规则、版本、摘要、有效期和理由。
4. 不同人员独立批准。修改建立新版本；批准新版本后旧版本失效。最长有效期24小时，从申请创建时计算，迟延批准不会延长原有效期。禁用来源、撤销、超时或能力变化不能继续作为当前执行依据。
5. 执行器下载当前有效策略并上报 `policy_loaded` 回执。该回执只证明执行器报告已加载，界面不把它等同实际阻断事件。
6. 每个受控动作先保存本地不可重复的 intent；内容在本机检查。正常动作先完成同步元数据审计，才调用原生接口，再保存完成或失败事件。敏感/未知决定不调用原生接口；阻断期间审计失败也不会放行。
7. 执行后审计离线时保存脱敏 outbox，明确报告“动作完成，最终审计待回传”，不得再次执行打印/复制来重试审计。`replay` 只发原事件，不调用终端动作。历史回传只表示原时点事实，不提升当前部署状态或自动关闭检查。
8. 冻结策略证据，导出 HTML/JSON：策略及独立决定、设备和来源范围、回执、实际事件、预算截断、缺口及快照摘要分别保留。反馈及后续状态不回写冻结快照。

## Windows 执行器

下载路径为 `/downloads/scarlet-endpoint-agent-windows-amd64.exe` 和 `/downloads/scarlet-endpoint-agent-windows-arm64.exe`，由应用镜像从同一源码构建。没有捆绑驱动，不自动注册服务、修改系统策略或安装证书。Windows Defender/系统签名检查仍由终端既有管理规则处理。

执行前在受保护的部署环境中配置以下值；令牌不要放在 URL、命令行参数、源代码或证据文件中。

| 环境变量 | 用途 |
| --- | --- |
| `SCARLET_OPERATIONS_TOKEN` | 平台一次性显示的该设备来源令牌 |
| `SCARLET_ENDPOINT_KEY_HEX` | 客户本地生成并保护的32字节HMAC密钥，64位十六进制 |
| `SCARLET_ENDPOINT_SCOPE` | 固定业务/部署作用域，1–80字符 |
| `SCARLET_ENDPOINT_DEVICE_ID` | 固定源端设备标识，1–256字符，只上传HMAC结果 |
| `SCARLET_ENDPOINT_SUBJECT_ID` | 可选源端主体标识，只上传HMAC结果 |

HMAC密钥、作用域和设备标识应在重启后保持一致；调整这些值需要登记新的来源绑定。平台不会获取这些原值。可以在管理员明确选择的交互会话中运行观察器；不要用Session0服务替代用户桌面剪贴板观察。

以下命令中的来源令牌和本地HMAC值均由环境提供；将 `https://platform.example` 替换为实际平台 origin，将策略编号替换为界面批准的编号。

```powershell
# 只登记/更新真实心跳，不操作数据
.\scarlet-endpoint-agent-windows-amd64.exe -url https://platform.example -mode heartbeat

# 明确运行原生观察，原内容不写入平台证据
.\scarlet-endpoint-agent-windows-amd64.exe -url https://platform.example -mode watch -state-dir C:\Scarlet\state

# 下载已批准策略并报告加载；不代表已经发生阻断
.\scarlet-endpoint-agent-windows-amd64.exe -url https://platform.example -mode sync -policy 123 -state-dir C:\Scarlet\state

# 显式受控动作：每次使用新的业务操作ID，禁止重用不确定的操作ID
.\scarlet-endpoint-agent-windows-amd64.exe -url https://platform.example -mode usb -policy 123 -operation-id export-20261113-001 -state-dir C:\Scarlet\state -from-root C:\Approved -source contacts.json -usb-root E:\ -destination contacts.json
.\scarlet-endpoint-agent-windows-amd64.exe -url https://platform.example -mode print -policy 124 -operation-id print-20261113-001 -state-dir C:\Scarlet\state -from-root C:\Approved -source notice.txt -printer "Corporate Printer"
.\scarlet-endpoint-agent-windows-amd64.exe -url https://platform.example -mode clipboard -policy 125 -operation-id copy-20261113-001 -state-dir C:\Scarlet\state -from-root C:\Approved -source notice.txt

# 回传待交付的原始脱敏事件；不重新执行终端动作
.\scarlet-endpoint-agent-windows-amd64.exe -url https://platform.example -mode replay -state-dir C:\Scarlet\state
```

客户端与平台仍须保持受支持范围内的时钟同步。下载策略后，以平台签发/批准时点和本地单调经过时间构建回执时间及本地有效期截止点，避免客户慢钟延长策略TTL；首次心跳及超出时钟偏差范围仍保持拒绝，不跳过时间门禁。

`-local`仅允许显式本机回环 HTTP 验收，不关闭TLS校验。HTTP重定向不携带来源令牌继续跳转。原生接口错误不包含来源正文或路径；失败可能已有部分副作用，证据按实际阶段记录，不标记成功或预阻断。

## 内容与证据边界

受控动作输入上限1MiB，使用现有有界TXT/CSV/JSON/JSONL解析；未知格式、截断、非法编码、无法覆盖的保护类别不能在敏感阻断模式下放行。监测模式允许受控动作，但不把未知内容解释成安全。打印使用有界GDI文本分页，不发送任意RAW打印语言；剪贴板只显式写Unicode文本，并设置Windows历史/云剪贴板排除格式。Windows这些格式的定义见[官方Clipboard Formats](https://learn.microsoft.com/en-gb/windows/win32/dataxchg/clipboard-formats)。

平台事件只保留分类、通道、策略摘要、来源内主体/设备/对象/动作引用、执行器和阶段。受控输入内容摘要使用客户侧HMAC；不上传文件正文、原文件名、路径、打印机名称、剪贴板文本、账户名或HMAC密钥。普通观察的缺失、不完整及采样预算不会补零。正常活动不创建风险待办，已阻断/未知/失败的风险证据单独保留，也不被后续允许活动自动清除。

本地state目录只保存元数据outbox及不可重复intent，应由终端管理员限制ACL并按审计保留要求归档。intent存在时不自动重试原动作；先核对真实终端结果，才能用新的业务操作ID申请新的动作。

## 验收状态

验收材料区分三层：平台真实PostgreSQL/HTTP工作流、真实Win32调用源码与交叉编译、真实Windows设备上的最终运行。前两层通过不能替代第三层；实际USB硬件、打印机/虚拟打印机、交互剪贴板和权限失败应在目标Windows环境用合成数据分别验收，不需要采集用户个人内容。
