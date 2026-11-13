# 本地文件内容识别与具体值调查

文件正文、图片、解压后的文字与具体值始终在客户侧处理。平台保存匿名文件资产、版本化命中、有限位置与覆盖，以及可选的 HMAC 等值指纹。识别到格式不等于核实真实主体、业务用途或法定类别；相同指纹也不证明两次访问之间存在真实数据血缘。

## 文件来源

复用发现来源、领取租约、规则版本、测试/采集、周期及来源失效机制。创建来源使用现有 `POST /api/v1/workspaces/{slug}/discovery/sources`：

```json
{"source":"已授权文档目录","provider":"local_files","system":"local_files","database":"合同资料集","scopes":["."],"intervalMinutes":60,"enabled":true}
```

`database` 是逻辑文件集名称；`scopes` 是客户本地根目录下的相对目录，不能含上级路径或绝对路径。平台不能指定任意主机目录。客户配置 `SCARLET_FILE_ROOT` 为实际根目录，继续使用现有 `SCARLET_URL`、`SCARLET_DISCOVERY_TOKEN` 及 `discovery-agent`。根目录、子目录及文件的符号链接不会被扫描；只读取普通文件，打开操作使用限制在根内的 `os.Root`。

默认支持 UTF-8 TXT、CSV、JSON/JSONL、DOCX 主文档文字、PPTX 幻灯片文字及有界 XLSX 静态单元格值。CSV 定位到行/列序号，JSON 定位到值序号，文档定位到段落序号，XLSX 定位到匿名工作表序号与单元格坐标；不上传可能包含业务值的字段键、文件名或路径。文件资产名称为匿名 `file-<标识前缀>.<扩展名>`，目录类型为 `file`，完整受限证据位于 `metadata.fileEvidence`。人工确认和发布继续使用分类工作台；文件命中当前作为候选，不自动认定法定类别。

文件路径标识及 `contentDigest` 使用客户侧来源专用密钥 HMAC，内容未读取时摘要为空。当前采集器使用本次来源令牌作为摘要密钥；轮换令牌会改变匿名文件身份，旧证据仍保留。`contentDigest` 只用于同一来源密钥下的内容版本对比，不能与其他来源直接对比。平台同时保存扫描采用的完整规则版本；规则变化后旧观察需要重采。

原有关键词、RE2 正则及精确词典沿用同一匹配器。文本中内置格式先提取候选，再执行邮箱/手机号/身份证校验位/银行卡 Luhn 校验。精确词典匹配整个单元值；不会隐式变成模糊词典。任意自定义类别仍为候选证据。

## XLSX 单元格范围

XLSX 使用 Go 标准库 ZIP/XML 在客户侧只读解析，不依赖 Office、Excel 自动化或外部服务。按照工作簿内部关系及工作表顺序读取 sharedStrings（包括普通/富文本，忽略注音文本）、inlineStrings 和数值单元格；未被单元格引用的共享字符串不作为数据命中。共享字符串索引与可选内联字符串的处理依据 [Microsoft 单元格取值说明](https://learn.microsoft.com/en-us/office/open-xml/spreadsheet/how-to-retrieve-the-values-of-cells-in-a-spreadsheet)和 [共享字符串表说明](https://learn.microsoft.com/en-us/office/open-xml/spreadsheet/working-with-the-shared-string-table)。

每个位置保存 `unit:"cell"`、遍历序号 `index` 及 `locator:"sheet:1:cell:A1"`；工作表只使用序号，原工作表名称不上送。值指纹使用相同 locator，可在现有具体值调查中核对工作表和单元格。`column_N` 表示工作簿内列序号，同列的格式统计可能来自不同工作表，逐项位置用于进一步区分。旧 TXT/CSV/文档的位置及指纹协议保持兼容。

公式不执行，含公式的单元格连同缓存结果一起跳过并记录 `xlsx_formula_not_evaluated`。外部关系、外链工作簿、XML 实体均不访问。宏、ActiveX 或嵌入对象存在时返回 `xlsx_unsupported_parts`，该文件内容状态为未知；不尝试执行或解析对象。普通单元格范围以 `xlsx_cell_values_only` 标为有限覆盖，不能把未命中解释为整份工作簿没有敏感信息。

数值按 XML 中的存储文本识别，不执行公式、样式、日期或科学计数显示格式转换，也不恢复 Excel 已丢失的精度。存在样式或需显示格式转换时提示 `xlsx_number_format_not_applied`。不支持的单元格类型、不可用共享字符串索引或超过单元长度预算的部分明确跳过并保留缺口。

除通用文件预算外，XLSX 最多读取前 20 个工作表，合计检查 200 个单元格（含跳过的公式），共享字符串表最多保存 2,000 项；所有相关 XML 的实际读取总量及采用单元格文本总量分别不超过 256 KiB。ZIP 门禁与 DOCX/PPTX 共用：拒绝路径穿越、绝对/反斜杠路径、符号链接、重复或大小写别名项，限制 2,000 项、单项声明 8 MiB、总声明解压量 32 MiB，并继续限制实际解析字节。ZIP/XML 异常和预算限制均不会借用上一批命中作为当前结果。

本轮只增加 `.xlsx` 发现识别；`.xls`、`.xlsm`、`.xlsb` 及任意 ZIP 仍不进入该解析器，不表示这些文件已检查或不敏感。实际外发 guard 的四类文本解析范围没有扩大，遇到 XLSX 继续按原有不支持格式保持未知，未因此增加外发许可。采集器和平台需要同时升级；未升级的旧采集器仍可能跳过 XLSX，不能据其批次宣称已完成 XLSX 识别。

## PDF 与图片 OCR

PDF 需要在客户采集机安装 Poppler，并显式加入 `-file-pdf`。当前提取前 20 页的文本层；扫描 PDF 无文本时显示未知，不宣称已 OCR 整份 PDF。PNG/JPEG 需要本地 Tesseract 并显式加入 `-file-ocr`；语言由 `SCARLET_FILE_OCR_LANGUAGES` 指定，默认 `eng`，语言包必须实际安装。

```sh
go run ./cmd/discovery-agent -once -file-pdf -file-ocr
```

原始文件通过本地子进程标准输入传递；提取结果仅在采集进程中匹配，不写平台或运行日志。子进程超时 8 秒且输出受限。OCR 结果保留 `ocr_requires_review`：文字可能被误读，格式检查不能弥补 OCR 识别错误。依赖未安装、未启用、文件加密、解析失败、文本层缺失等均显示明确缺口。

PDF 文本提取和图片 OCR 分别依赖 [Poppler 项目](https://poppler.freedesktop.org/)与 [Tesseract 官方命令文档](https://tesseract-ocr.github.io/tessdoc/Command-Line-Usage.html)。本轮没有将外部 OCR 服务或模型推理接到文件正文。

每次最多 100 个文件、遍历最多 10,000 个路径、单文件最多 8 MiB、提取文字最多 256 KiB、最多 200 个单元。压缩文档最多 2,000 项、解压声明总量 32 MiB、单项 8 MiB，并继续限制实际读取和提取输出。单元超过 4,096 字节跳过，命中位置最多 200 项。整个扫描超时 60 秒。预算限制不代表没有其他敏感数据；负向结果仅代表已覆盖部分未命中。

## 具体值指纹与查询

客户配置以下三个环境变量后，文件采集与授权的运行时 JSON 采集可生成具体值指纹：

- `SCARLET_VALUE_KEY_HEX`：客户本地生成并保护的 32 字节密钥，编码为 64 个十六进制字符；不提交平台。
- `SCARLET_VALUE_KEY_ID`：密钥标识；轮换密钥时更换标识。
- `SCARLET_VALUE_SCOPE`：工作区内明确约定的数据域标识。需要对比的客户侧采集点应使用同一密钥、标识及数据域。

算法为 `HMAC-SHA256(key, "utf8_exact_v1" + NUL + scope + NUL + value)`。v1 使用精确 UTF-8 字节，不删除空格、不转换大小写，也不尝试解密。邮箱、手机号等经格式校验的观察可携带指纹；文件每项及运行时每次观察最多 32 个。指纹是敏感关联标识，工作区成员权限仍受约束。

在客户机运行 `go run ./cmd/value-fingerprint -key-id <密钥标识> -scope <数据域>`，从标准输入读取精确值，输出下列查询 JSON。不要将值放命令参数、URL、日志或 shell 历史；输入末尾换行属于精确值的一部分。

```json
{"keyId":"client-key-1","scope":"workspace-data-domain","version":"utf8_exact_v1","digest":"64位十六进制HMAC"}
```

页面只粘贴该 JSON。`POST /api/v1/workspaces/{slug}/investigations/value-search` 只接收这四个字段及可选 `sourceId`，不接受原值。当前 owner/operator/reviewer 可查询。结果 `items` 为发现文件命中，包含 source/run/asset/locator、观察时间及 current；`runtimeItems` 为最近保留期内的运行时事件匹配，包含服务、接口、主体引用、观察到的 IP、字段及审计记录引用。`sourceId` 过滤仅用于文件发现来源，此时不混入运行时来源。

文件指纹随成功采集在同一事务中保存，通过任务令牌、来源修订、领取的规则及租约校验；调用方不能指定任意资产写入。运行时指纹只能对应同次实际记录的字段名称与类别。查询不会跨工作区关联，也不会将来源已经暂停、过期或修订的历史文件证据当成当前有效。

旧日志没有指纹时无法追补具体值；不同 keyId/scope/规范化版本无法直接比较。没有命中不证明该具体值从未被访问。值指纹不提供原文恢复、通用加解密、完整请求正文或任意应用内部插桩。

## 显式加密/解密转换关系

`backend/pkg/cryptoevidence` 提供客户侧 `Recorder.Transform`。客户在已授权的真实加密/解密调用处使用自己的函数，SDK 在调用前后分别生成 HMAC，返回给应用的实际输出仍留在应用；只有独立 `Record` 可以提交平台。记录含 eventId、traceRef、spanRef、observedAt、operation、algorithm、input/output Token。算法名称是源端声明，SDK 不提供通用解密器。

明文 Token 使用上述 `utf8_exact_v1`；密文字节使用 `bytes_exact_v1`，算法域前缀相应变为 `"bytes_exact_v1" + NUL + scope + NUL + ciphertext`。两个 Token 的 keyId 和 scope 必须一致。明文限 4,096 字节、二进制限 8,192 字节，超过预算不记录这项转换证据。

客户先提交相应运行时观察，再以同一已授权 runtime Bearer 令牌向 `POST /api/v1/runtime/crypto-transforms` 提交 `{kind:"gateway"或"otlp",records:[...]}`，单次最多 32 项、128 KiB。服务端要求当前来源未撤销、创建人仍为 owner，并在**同一来源**已保存审计中查到相同 trace/span 且事件时点相差不超过 5 分钟；上报事件自身须在近 5 分钟内。缺少审计绑定时拒绝，调用方不能指定其他来源或任意资产。eventId 重放只接受相同内容，修改声明返回冲突。

平台仅保存审计引用、两侧匿名 Token、算法/操作及时间。值查询的 `relations` 返回命中 Token 所在同来源、同 trace、同 keyId/scope 的显式转换关系；保留 `customer_instrumented_transform` 标记。关系随对应审计的保留和删除机制清理，来源撤销后历史关系显示 current=false。

这能够记录一次已授权插桩声明的转换关联，**不能**由格式可见或关系存在推断数据库未加密、加密有效、密钥治理合规或已经识别真实自然人。缺少插桩时保持未知，也不承诺参考材料所述的任意业务系统免改造加解密探针。实际 Go AES-256-GCM 合成测试覆盖 encrypt/decrypt、输入/输出 Token 对应、无明密文或密钥序列化、审计绑定与跨工作区隔离。

PostgreSQL 托管采集另可保存 `table.transport`：由本次真实 pgconn 连接读取 TLS 状态、协议和密码套件，scope 固定为 `collector_to_configured_postgres_endpoint`。该证据不读取密钥，也不从 SSL 配置或内容类型推断结果；旧记录缺少 transport 时为未知。当前来源/修订/新鲜度有效后可以说明这一连接跳段，仍不能据此推出数据库静态加密或完整链路加密有效。

## 本轮验收范围

隔离 PostgreSQL 中验证真实本地采集、租约提交、文件资产/候选、精确值搜索、来源失效及跨工作区隔离。实际本机 Poppler 26.04 与 Tesseract 5.5.2 验证 PDF 邮箱与图片手机号。合成测试覆盖 TXT/CSV/JSON/DOCX、禁止符号链接/越界范围、压缩膨胀及单元预算、输出不含正文或原文件名。XLSX 使用实际生成且由独立 OOXML 读取器确认可打开的双工作表文件，验证共享/富文本/内联字符串、数值手机号、公式缓存跳过、匿名坐标、HMAC 查询及真实 Store Queue→Claim→Submit 流水线；下一批同路径换成恶意压缩文件后保持未知，原指纹仅作历史记录。相关入口为 `internal/collector/discovery_xlsx_test.go` 与 `internal/store/catalog_discovery_xlsx_test.go`。转换关系的实际验收入口为 `pkg/cryptoevidence/transform_test.go`、`internal/store/crypto_transforms_test.go` 和 `internal/httpapi/value_fingerprints_test.go`。上述验收不表示 OCR 或企业生产语料达到指定准确率，也不表示 DOC/XLS、扫描 PDF、任意压缩文件或嵌入对象已经支持。
