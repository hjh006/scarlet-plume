# 全知科技与亿格云数据安全能力差距映射

核对日期：2026-11-12。基线为本轮补差开始时的工作区源码，包含已完成的红途场景补差。本文件先记录官网宣称与实际工程差距，各项“本轮实现增补”说明之后新增的实际代码与验证；不能因写入本表就标为已完成。

外部材料只采用 `data-sec.com`、`eagleyun.com` 的官方产品页面。未读取商业案例本地资料，未使用演示环境、厂商账号或客户数据。官网是能力线索，不是独立验收、性能或精度证明；没有据此推断两家产品的内部架构或完整接口。

本轮补差仅限数据安全相关能力：数据识别、访问与外发证据、受控API验证及调查。通用终端防护、全套零信任/IAM、厂商原生客户端与内核能力不在本轮完成范围。

## 官方产品范围

| 官方产品与本次链接 | 官网可核对的能力方向（转述） | 对照时的工程边界 |
| --- | --- | --- |
| [全知知源](https://www.data-sec.com/product/zhiyaun/) | 多源静态资产识别、规则与AI协同、行业分类模板、知识辅助及人工校准、结果评审发布与同步 | 赤翎已有相应基础工作流；仍需逐来源、逐算法验证覆盖和实际下游写入，不能以名称相同表示等效。 |
| [全知知形](https://www.data-sec.com/product/Application/) | 数据库访问分析、账号与权限关系、风险行为审计、访问泄露回溯 | 当前页面正文名称为“数据库风险监测系统”，URL仍为Application；按当前正文核对，不把旧“应用审计”名称当成另一个已验证产品。 |
| [全知知影](https://www.data-sec.com/product/api/) | 流量发现API、敏感数据暴露面、接口弱点与行为风险分析 | 官网关于覆盖广度、学习算法和部署效果的描述不作为赤翎已实现证据。 |
| [全知知镜](https://www.data-sec.com/product/zhijing-home/) | 按任务进行资产、分类、API及异常行为检测，含终端文件与出境通道观察 | 需区分主动探测、旁路观察、终端Agent和人工事实，不能以一个场景检查按钮替代全部采集器。 |
| [亿格云XDLP](https://www.eagleyun.com/product/xdlp) | 外发通道识别、文件操作链、内容变化后的溯源、风险运营和Shadow AI治理 | 现有文件扫描、精确指纹、网关处置只覆盖其中部分；不等于终端全通道DLP。 |
| [亿格云AI-DLP](https://www.eagleyun.com/product/ai-dlp) | 事件语境、动作时间线、风险解释与处置建议 | 调研基线的AI能力用于分类；本轮事件参考增补见G05，协议可用不等于生产事件研判精度已验证。 |
| [亿格云AIDR](https://www.eagleyun.com/product/aidr) | AI工具、编程及Agent使用治理，包含输入输出、工具调用与敏感信息处理 | 网关可见流量与客户端/IDE/MCP原生观测是不同来源；当前未拥有这些终端插件和厂商执行接口。 |
| [亿格云AI-IRM](https://www.eagleyun.com/product/airm) | 跨来源事件关联、人员设备上下文、调查和响应反馈 | 行为关联需要可信主体映射、来源时钟与业务授权；不能从同IP或模型文字推断真实人员或恶意动机。 |

## 调研基线已有能力，避免重复造页

| 已存在的实际能力 | 主要代码 | 本次对照必须保留的限制 |
| --- | --- | --- |
| 多源目录、字段与文件发现，自有关键词/正则/词典及格式校验 | [discovery.go](../backend/internal/collector/discovery.go)、[discovery_files.go](../backend/internal/collector/discovery_files.go)、[catalog_discovery.go](../backend/internal/store/catalog_discovery.go) | 直接连接器覆盖PG、MySQL/MariaDB、MongoDB等已实现来源；其他数据库的OpenMetadata目录声明不等于对应原生内容/权限采集。文件有格式、页数和采样预算。 |
| 行业标准引用、人工分类、目录版本、独立发布与冻结导出 | [classification_workbench.go](../backend/internal/store/classification_workbench.go)、[classification_publications.go](../backend/internal/store/classification_publications.go) | 发布只覆盖明确选中的资产集合；来源/规则/分类修订失效不能沿用为当前可信证据。 |
| 可训练本地分类器、词法知识检索及用户自选AI协议 | [model.go](../backend/internal/classificationml/model.go)、[classification_knowledge.go](../backend/internal/store/classification_knowledge.go)、[classificationai](../backend/internal/classificationai/) | 本地模型为朴素贝叶斯，知识检索为词法算法；没有深度学习或向量检索效果基准。外部模型需显式授权，合成TLS验收只验证协议。 |
| 手动授权当前页字段标注，实际OpenAPI同名候选 | [browser_annotations.go](../backend/internal/store/browser_annotations.go)、[扩展源码](../extension/browser-field-labeler/) | 不抓输入值、后台流量或全站页面；候选由人工确认，不证明已观察到DOM与API血缘。 |
| 网关/OTLP观察、版本化运行时策略、API生命周期及受限主动验证 | [runtime_detection.go](../backend/internal/store/runtime_detection.go)、[runtime_policy_scope.go](../backend/internal/store/runtime_policy_scope.go)、[apivalidator](../backend/internal/apivalidator/) | 阈值窗口不是自学习行为基线。主动验证基线只有BOLA与已提供弱凭据；缺证明、连接模式或采集不完整为待核实。 |
| 精确值指纹与客户显式加解密转换证据 | [value_fingerprints.go](../backend/internal/store/value_fingerprints.go)、[cryptoevidence](../backend/pkg/cryptoevidence/)、[crypto_transforms.go](../backend/internal/store/crypto_transforms.go) | 同keyId/scope/version精确比较；不是变形文件识别、图像相似溯源或通用解密。 |
| 事件、人工风险研判、独立审批、客户侧执行与新来源复测 | [runtime_risk_triage.go](../backend/internal/store/runtime_risk_triage.go)、[control_actions.go](../backend/internal/store/control_actions.go)、[control_verification.go](../backend/internal/store/control_verification.go) | 执行种类目前为受约束的网关临时阻断和PG直接SELECT撤销；没有终端隔离、USB/打印拦截或任意厂商策略写入。 |
| Wazuh真实固定规则样例、通用事件桥、冻结报告和接收回执 | [event-bridge](../backend/cmd/event-bridge/main.go)、[Wazuh验收](wazuh-live.md)、[operations_reports.go](../backend/internal/store/operations_reports.go)、[classification_delivery.go](../backend/internal/store/classification_delivery.go) | 通用Wazuh投影刻意不接收任意data、主机身份和原文；固定真实样例不证明全部Wazuh规则均可关联。Webhook accepted只证明接收，不证明目标产品完成处置。 |

## 优先补齐的五项

以下是从官网方向和当前源码推导的工程建议，不是厂商对赤翎的认证。每项先实现数据路径、检查和回执，再复用现有结果页；不新增无数据的能力卡片。

### G01：终端与外发事件的结构化来源

对应[XDLP](https://www.eagleyun.com/product/xdlp)和[AI-IRM](https://www.eagleyun.com/product/airm)。调研基线的 `SecurityEventInput` 只有规则、严重度、计数及可选资产/接口范围；`event-bridge`的Wazuh模式仅投影告警ID、时间和规则等级。缺少设备引用、来源内主体、文件对象、动作、进程及通道之间可验证的关系。

最小实现是新增带版本的白名单事件契约与真实适配器，优先接一类已授权Wazuh/FIM或现有DLP产品事件。保存原生记录引用、事件时刻、投影版本和摘要；主体/设备使用客户侧去标识引用。只有源端明确给出的动作才进入时间线，不能把一次文件变更解释成已外发。

复用 `operations/security-events` 的身份、幂等与检查闭环；终端扩展字段不能塞进无结构的备注。验收需实际来源正/反例、重放、来源撤销与跨工作区隔离。Wazuh日志接入本身不提供文件内容识别，也不授予剪贴板、打印、USB或文件访问阻断能力。

**本轮实现增补：** [DataContext](../backend/pkg/datasafety/context.go)已经提供白名单结构，区分终端、数据库、AI及通道、操作、源端执行声明和采集完整度；主体、设备、对象等引用只接受64位十六进制摘要。`event-bridge`新增[`wazuh_context`](../backend/cmd/event-bridge/data_events.go)，从实际Wazuh FIM字段契约提取文件变更、类型、摘要及源端HMAC主体/设备/对象引用，省略路径、用户名和原始日志；FIM内容类别保持空、采集标记为partial，不解释为已外发。[适配器测试](../backend/cmd/event-bridge/data_events_test.go)使用合成原生日志形状验证确定性和无原文投影；[真实PG/HTTP测试](../backend/internal/httpapi/data_security_cases_test.go)覆盖幂等、范围过滤、来源撤销、跨工作区和权限。它们不是本轮新增部署全部终端通道的证明。

正常活动以`outcome=activity`保存而不创建G03风险待办或自动风险通知；允许的复制、AI出口与正常pgAudit记录不是风险命中，也不清除既有风险。阻断和未知决定仍使用`observed`进入风险取证链，FIM高/严重级告警也保留其风险声明。

### G02：Shadow AI与已授权AI通道的数据外发检查

对应[AIDR](https://www.eagleyun.com/product/aidr)和[XDLP](https://www.eagleyun.com/product/xdlp)。调研基线的AI profiles配置分类模型，不是企业员工AI工具目录；通用运行时context可以接收源端明示的目的地域名、接收方与数据量，但没有AI用途标识、提示词/附件通道或工具/MCP权限模型。

先做用户维护的AI工具/目的地目录，明确批准状态、来源绑定和版本；由已授权网关或应用SDK识别真实AI请求及有界内容统计，再采用已有数据类别、目的地和量限制规则。只在具有执行器且目标精确匹配时申请受控动作。分类模型连接许可不能复用为员工数据外发许可。

验收需批准/未批准目的地、允许/超范围输入、未检查完整和新来源复测。未经过该网关的桌面客户端、IDE插件、MCP和Skills保持未覆盖；需要各自真实代理/SDK/终端适配器，不能从域名命中宣称识别了提示词语义、代码窃取或提示词注入。

**本轮实现增补：** [`pkg/datasafety`](../backend/pkg/datasafety/guard.go)提供源端策略SDK：按明确批准的AI域名、保护类别和允许通道返回allowed、blocked或unknown；AI类型与`ai_prompt`通道双向校验，采集不完整不产生允许结论。[受控文本复制CLI](../backend/cmd/data-transfer-guard/main.go)会真正拒绝被策略阻止的本次指定复制，使用`os.OpenRoot`限制相对路径、拒绝覆盖现有文件，事件只含类别、文件摘要和决策。它只接受有界txt/csv/json/jsonl，JSON/JSONL实际解码转义标量，CSV实际解析引号与记录；不支持格式、已知容器签名、解析不完整或策略要求未实现的类别均为unknown并拒绝复制。当前本地格式识别只有邮箱、手机号、身份证校验位、银行卡Luhn四类，不宣称识别所有敏感语义。原文件字节及摘要保留，正文不进入事件。实际文件复制的阻断/允许、改名容器、Unicode转义、预算和根路径边界已由[本地真实文件测试](../backend/pkg/datasafety/guard_test.go)验证；AI目的地策略由相同SDK测试验证。此CLI和SDK没有安装系统钩子，不提供全局USB、打印、邮件、剪贴板或AI客户端拦截；这些通道仍需有执行权限的真实适配器。

文件采集器另外支持[有界XLSX静态单元格识别](file-content-and-value-evidence.md#xlsx-单元格范围)，不执行公式或跟随外链，并明确缺口。采集能力与外发许可分开：复制guard仍仅支持TXT/CSV/JSON/JSONL，XLSX继续unknown并拒绝复制。

[`runtime-agent`受控AI出口](../backend/cmd/runtime-agent/ai_egress.go)进一步把策略接入真实网络转发：显式同时启用`-ai-egress -data-policy`，仅检查配置路由POST JSON，固定HTTPS上游域名作实际目的地。保护类别或未批准域名命中时转发前403；无法完整检查时拒绝；允许请求须先成功同步投递脱敏决定，再把原body字节交现有代理。上游全部3xx被拒绝并返回无`Location`的安全502，阻止客户端跟随307/308向另域重放正文；非重定向SSE仍即时流转，默认非AI代理行为不变。[本机HTTP/TLS正反测试](../backend/cmd/runtime-agent/ai_egress_test.go)验证被保护内容和未批准域名没有抵达上游、允许内容字节一致，压缩/未知/截断/超限/审计失败也不转发；独立有效TLS目标验证307/308无正文重放及SSE首块及时到达。该模式不检查AI响应正文、全局客户端或编码附件，规则格式未命中不能推导所有语义安全；运行步骤和预算见[运行时接入说明](runtime-security.md#显式启用受控ai出口)。

### G03：数据库活动审计，而非仅元数据与ACL

对应[知形](https://www.data-sec.com/product/Application/)。PG元数据采集、直接权限快照和连接TLS证据已经存在；OTLP数据库目标也可用于已观察关系。但调研基线没有通用数据库账号会话、SQL语义操作、查询/返回量、访问结果及其连续审计事件来源。

先实现一类实际PG审计日志适配，再补MySQL审计来源；需要客户明确开启对应日志/插件并限定采集范围。只上传允许的操作语义、对象引用、计数、结果、去标识账号及原始审计记录引用，SQL字面值和原始结果留在源端。账号与业务人员的映射必须来自明确授权的目录。

可复用现有来源、幂等事件、规则窗口、证据与报告，不把数据库审计事件强塞为HTTP请求。验收覆盖实际SELECT/拒绝访问/失败操作、账号隔离、日志丢失和数量未知。旁路协议解析、多数据库加密流量还原、继承权限有效访问与完整SQL审计仍需各自原生适配；不能由读取pg_catalog或一次OTel span替代。

**本轮实现增补：** [`event-bridge -format pgaudit`](../backend/cmd/event-bridge/data_events.go)已解析PostgreSQL JSON日志信封中的pgAudit CSV记录，保留时间、操作类别，以及源端HMAC处理的账号、数据库、会话、对象和语句引用；原SQL、参数、数据库名和账号名不上传。支持的日志类别映射read/write/execute，未知返回量不补零，采集标记为partial、执行仅为observed。与FIM相同的[合成日志适配测试](../backend/cmd/event-bridge/data_events_test.go)验证了CSV解析、稳定引用与原文排除；这证明解析器和入站契约，不代表已在客户数据库启用pgAudit、持续接入生产日志或验证所有失败操作。MySQL等其他原生审计源、日志缺失探测、结果数量及完整审计覆盖仍是差距。

### G04：扩展有适用证明的API主动用例

对应[知影](https://www.data-sec.com/product/api/)和[知镜](https://www.data-sec.com/product/zhijing-home/)。本轮调研基线中，`apivalidator.Binding`和 `validAPIValidationInput` 只支持 `bola`、`weak_credential`；`validationRuleOutcome`只对相应完整对照接受确定结果。被动SQLi/XSS/路径模式也没有自动成为可利用漏洞的依据。

复用当前仅GET、批准目标、固定路径/对象与客户侧凭据的manifest，增加明确认证要求下的匿名访问对照、指定敏感字段的有界内容观察、显式响应头或CORS基线等用例。每类要有适用声明、实际请求、正向对象证明、可观察分母、采用规则和缺口；不具备这些条件时返回inconclusive。

新增规则须贯穿计划校验、来源执行、结果验真、固定规则身份、检查取证、相同目标新运行复测和导出。修改计划后独立审批失效，旧证据不改写。本轮已按上述约束增加三类GET用例并通过源端真实HTTP及隔离PG独立审批/复测链验收，详见[API验证说明](api-validation.md)。这缩小了G04差距，仍不是厂商全部测评能力；不能直接写“全OWASP覆盖”、无授权扫描或自动漏洞利用。

### G05：事件关联、反馈与可选AI研判

对应[AI-DLP](https://www.eagleyun.com/product/ai-dlp)与[AI-IRM](https://www.eagleyun.com/product/airm)。调研基线的人工triage保存单个finding的等级、责任、到期日和理由；报告可冻结所选窗口。缺少跨规则/来源的case、版本化关联依据、事件去重/合并、误报反馈与面向安全事件的AI参考。

先按可信来源内的主体/设备/对象及时间窗建立确定性关联，保存每条边的原生引用与解释；IP相同只能形成候选。case冻结事件及规则版本，允许人工标记已确认、误报、待核实，并保存理由及修订。复用现有检查、工单交付和独立处置，不自动把算法分数变成执行授权。

有真实case证据后才复用用户自选AI连接，为最小化的事件投影生成明确可选的参考摘要。模型输出必须引用现有证据ID、列出缺口并由人采用；不开放自由SQL检索或任意工具执行，也不将模型推测写成员工动机、离职状态或确定责任。验收需规则去重、错误身份合并反例、来源断链、反馈版本及模型超时/无引用时的降级路径。

**本轮实现增补：** [数据安全cases](../backend/internal/store/data_security_cases.go)已经按明确来源、去标识主体/设备、通道和时间范围在SQL过滤后取最多200条事件，并冻结快照、摘要和`source-bound-data-events-v1`关联依据。同一来源的主体引用优先、缺少主体时采用设备引用；没有引用的事件单独保留，不做跨来源人员合并。人工追加待调查、预期活动或误报反馈，反馈不关闭原检查；HTML/JSON导出同时保留冻结证据与后续注记。[HTTP与Store权限测试](../backend/internal/httpapi/data_security_cases_test.go)覆盖独立范围、不可变快照、反馈、来源撤销和导出转义。

可选[事件AI参考](../backend/internal/classificationai/data_events.go)复用用户选择的Chat Completions或Responses兼容连接，要求hybrid、启用的连接、元数据授权及逐次确认；默认local不外联，保存时再次核对引擎模式和连接版本。发送内容只有`EventFact`白名单，不含主体/设备/对象/文件摘要、路径、SQL或提示词；模型返回只能引用已有事件ID，严格限制字段与长度，不执行工具，不自动采用或处置。[两协议实际TLS合成测试](../backend/internal/classificationai/data_events_test.go)验证精确请求投影和拒绝无效引用、未知字段、超长文本、密钥回显及工具输出；真实PG/HTTP用合成TLS模型贯通同一授权链。这是协议和数据最小化验收，不是生产模型研判精度、真实身份判断或完整IR/终端DLP的证明。跨来源可信身份目录、HR/IAM语境、变形文件关联及来源原生执行器仍未由该case能力补齐。

## 其他明确差距及落地顺序

| 差距 | 当前已有什么 | 缺少的真实实现与验收门槛 |
| --- | --- | --- |
| 更多原生数据源和语义分类 | 现有直接连接器、目录导入、本地训练和可选外部AI | Oracle、SQL Server、DB2等原生内容/权限采集须各自adapter与测试账号；模型训练、语义识别或向量知识检索须有授权语料、留出集、误报漏报与吞吐测试。不能将新增provider下拉项当完成。来源方向见[知源](https://www.data-sec.com/product/zhiyaun/)。 |
| 外部系统标签/策略真正落地 | 已发布快照、冻结交付载荷、目标绑定agent与接收ack | 每个目录/DLP/数据平台的实际写接口、认证、字段映射、幂等、目标版本读回和失败补偿；ack不代表标签已经可读。公开产品页未给出本仓库可验证的厂商API schema，本次不能编造“已支持全知/亿格云直连”。 |
| 文件变化后的源头识别 | 源端文件HMAC、精确值指纹、显式转换关系 | 可信文件创建/编辑/复制/上传事件、稳定对象身份和父子关系；局部片段/近似指纹及图像相似只能先作为候选，需对变形正反例测精度，不承诺任意截图/重排/改写可恢复。来源方向见[XDLP](https://www.eagleyun.com/product/xdlp)。 |
| 自学习异常基线 | 明确阈值、时间窗、主体/IP及来源明示地域规则 | 独立训练/观测窗口、最小样本、基线版本、季节性、异常反馈、漂移与回滚；无历史或突变不能默认正常。当前不宣称复现[知影](https://www.data-sec.com/product/api/)的学习算法。 |
| 内外部接收方、HR/IAM业务背景 | 来源可明示recipient/domain/country，活动可登记业务安排 | 带版本的组织/身份/外部接收方目录及授权映射；第三方邮件、IM、网盘等真实事件API。不能用邮箱后缀、IP或员工字段名直接推断关系与恶意。参考[AI-IRM](https://www.eagleyun.com/product/airm)。 |
| 提示词/附件实时脱敏与工具权限 | 分类请求白名单投影、密钥加密、无任意模型工具执行 | 真实受控代理输入输出管线、原格式保留、脱敏/还原映射本地托管、失败关闭/业务降级、MCP工具身份与审批；需要每类协议的adapter。现有分类建议不是[AIDR](https://www.eagleyun.com/product/aidr)级AI运行防护。 |
| 可扩展执行面 | 已授权网关block_api、PG revoke_select，独立审批和复测 | 厂商DLP/EDR/SWG/NAC等命令与回执adapter、范围证明、TTL/回滚和新来源验证。不得接任意命令、任意SQL或把Webhook任务标成已经阻断。 |
| 全量/长期效果对标 | 有预算和保留期限的文件/运行时证据、周期冻结报告 | 真实部署的采集率、丢失/重复、存储预算、吞吐及查询延迟、生产语料精度；长期归档和权限分离另行验证。产品宣传的全量、低误报、无感知或高准确率不能转作验收指标的既成事实。 |

## Wazuh与网关的可复用途径

Wazuh先作为**真实来源适配器**使用：仅启用有实际原生字段契约和测试证据的事件类别，保留其原生ID/源观察时刻。当前固定导出检查的真实闭环继续保留，不扩大原规则映射；新的FIM或DLP语义须单独适配、明确未知状态。Wazuh manager存在不等于终端Agent已经部署，也不等于已获得文件内容或通道拦截能力。

网关先作为**明确流量范围的观察/执行器**使用：在客户明确配置的服务、路由与跳段采集有界统计；审批后的动作仍须与实际来源和目标对应。账号/IP级问题不能静默扩大为整个接口阻断。第三方WAF/网关只在完成原生API、权限、回执和回滚适配后才列为可执行。

最小交付顺序为：G01/G02补可信事件与AI通道来源，G03补数据库活动来源，G04补有证明的主动用例，G05串联已存在的证据。可以并行开发，验收状态必须按每条数据路径分别更新；终端内核级能力、全协议旁路还原和厂商Agent/SDK全套保留为独立工程，不以本平台菜单、模拟响应或单一受控样例替代。

本调研未运行厂商产品测试，未建立跨产品同条件性能、覆盖或精度基准，也未访问客户环境。G01—G05的增补只描述本轮列明代码和测试覆盖的路径，不能据此把其余调研差距、终端通道或厂商集成标成已完成。

## 2026-11-13 终端通道补差

在上述调研基线上增加USB、打印、剪贴板的Windows原生观察器和显式受控动作执行器，以及平台设备绑定、版本策略、独立审批、加载回执、源端实际操作事件和冻结证据导出。可移动卷变化、打印队列、剪贴板新变化只作为观察；受控复制、GDI提交与显式Unicode剪贴板写入在执行前检查。下载与加载不证明全系统拦截。Windows二进制经过交叉编译，真实Windows终端实测仍待验收；内核级全应用覆盖、全部USB设备或格式仍不是本轮完成事实。详见[终端接入范围](endpoint-channel-enforcement.md)及[原生观察说明](windows-native-endpoint-observation.md)。
