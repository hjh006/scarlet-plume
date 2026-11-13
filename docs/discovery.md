# 多数据源发现、识别规则与统一目录

赤翎的 DDLC/DAI 切片由客户侧 `discovery-agent`、平台任务、版本化识别规则与统一目录组成。现有 PostgreSQL 托管采集继续使用自动采集页的连接配置、测试与计划。多源任务在“数据发现”页配置，无需向平台提交数据库连接凭据。

## 接入与权限边界

1. 工作区 owner 创建来源，设置接入方式、实际数据库类型、数据库名、授权范围、周期（1–1440分钟）。平台只保存这些非秘密配置。
2. 保存新任务时显示一次任务令牌。在客户环境中配置 `SCARLET_URL`、`SCARLET_DISCOVERY_TOKEN` 与 `SCARLET_DISCOVERY_DSN`。OpenMetadata 改用 `SCARLET_OPENMETADATA_URL`、`SCARLET_OPENMETADATA_TOKEN`。
3. 在 `backend` 目录运行 `go run ./cmd/discovery-agent -poll 15s`。生产连接要求验证证书和主机名的 TLS。只有显式 `-allow-insecure-loopback` 才允许字面回环地址的明文开发连接。单次运行用 `-once`。
4. UI 的“测试连接”排队执行实际只读发现并记录覆盖，不写入目录；“立即采集”排队写入目录。客户采集器必须在线。后台按平台计划轮询领取任务，2分钟租约防止并发领取。
5. owner 可暂停来源或更换令牌。更换令牌立即撤销旧令牌及在途租约。来源修订后旧目录身份不再用于精确数据流关联。任务创建人不再是 owner 时，令牌也不能领取或提交。

凭据保存在客户侧环境或服务配置，采集端日志只记录来源名与通用结果，不记录连接字符串、源端错误详情或样本。平台写接口要求当前工作区的 owner/operator；来源和令牌管理要求 owner。只读角色可查看被授权工作区目录。令牌只能提交其绑定来源、工作区及已领取修订的结果。

## 适配器能力和验收范围

| 接入方式 | 深元数据 | 内容识别 | 权限 | 血缘 | 验证证据 |
| --- | --- | --- | --- | --- | --- |
| PostgreSQL 原生 | schema、表、字段、类型、注释、约束、可见ACL、RLS范围 | 现有源内格式聚合；客户侧自定义RE2/词典受限样本 | 现有可见授权，受账号范围限制 | 未采集，unknown | PostgreSQL18、SCRAM只读角色，真实样本验证 |
| MySQL / MariaDB 原生 | 数据库、表、字段、类型、注释、可空性、约束 | 客户侧关键词/RE2/精确词典，样本不出客户环境 | 当前账号SHOW GRANTS；不等于全体主体有效权限。MariaDB认证条款从授权字符串移除 | 未采集，unknown | MySQL8.4与MariaDB11.8真实Docker验证；使用同一标准驱动与独立样本入口 |
| MongoDB 原生 | 集合、JSON Schema validator声明字段及说明、受限文档字段路径、类型 | 客户侧受限文档，嵌套对象路径；数组元素不推断 | 未采集源ACL，unknown | 未采集，unknown | MongoDB8.0真实Docker通过；只读角色、嵌套对象和词典命中 |
| OpenMetadata 深目录 | service/database/schema/table/column、嵌套字段、注释、标签、约束 | 不读取源数据值，标签与元数据规则仅为候选 | 源数据库ACL unknown；OpenMetadata owners不当作源ACL | `/v1/lineage/table/{id}` 一跳边，权限拒绝记unknown | 真实OpenMetadata2.0.7与官方PostgreSQL采集工作流已联调；新增源表/视图自动同步、真实401使旧批次失效、目录证据及报告通过；其他源系统仍须分别验收 |
| 本地文件 `local_files` | 匿名文件资产、格式、大小、修改时间、来源专用HMAC摘要 | TXT/CSV/JSON/JSONL、DOCX主文档与PPTX文字；显式本地Poppler PDF文本层、Tesseract图片OCR | 不推定文件有效ACL，unknown | 不推定业务血缘，unknown | 真实本地提取、隔离PostgreSQL资产/候选/指纹查询；原名、路径及正文不上送，格式和预算边界见[文件与指纹说明](file-content-and-value-evidence.md) |

SQL Server、Oracle、DB2、Hive、ClickHouse、Snowflake、BigQuery、Redshift、Trino、Presto、Spark、Databricks 通过客户已有 OpenMetadata 连接器进入同一深元数据适配链路。平台任务须填写实际系统类型并严格绑定 service/database/schema；返回的serviceType必须与任务声明一致，缺失或矛盾时拒绝该批次。此链路不是这些数据库的原生连接驱动；能见字段取决于客户 OpenMetadata 版本、权限及其实际采集结果。不能从元数据服务所有者推断数据库授权，不能从同名字段推断精确访问关系。

每轮最多100表、10,000字段；单表最多1,000字段。原生内容采样最多前10表、每表200行，MySQL及PostgreSQL自定义采样最多前16字段，每个值最多256字节。MongoDB字段推断最多8层，不穷举数组结构。前缀样本不是随机抽样、全库扫描或人数统计。读取失败、预算、格式和权限限制保留在coverage/warnings。原生连接均为只读操作；MySQL/MariaDB使用只读一致性事务。

## 识别规则和分类

owner/operator 可管理关键词、RE2正则、精确词典规则，规则明确元数据或内容范围。草稿不会下发；发布替换同标识的上一发布版本，旧版本与规则命中证据保留。每项命中保存规则标识、版本、方法、匹配数、扫描数、库表字段范围、来源批次与覆盖，结果协议无原值字段。负向结果只表示本次样本未匹配。

内置邮箱、手机号、身份证校验位、银行卡Luhn格式，以及个人信息字段名候选；原生采集共享格式校验，PostgreSQL保留既有源内聚合。已验证的原生数据库内置格式统计进入现有分类标准引擎，采用工作区默认或资产已选标准，保留缺失条件。自动判定只升级可确定的个人信息属性与业务等级；人工确认不被自动覆盖，负向采样不会降级，重要/核心等法定类别仍需独立取证。任意客户自定义类别和文件命中首先作为候选证据，不借用未满足条件的法规条款作结论。

## 目录、修订与CMDB

数据库、表、字段以及CMDB的业务/应用均使用 `plume.assets.id`。目录保存来源身份、父引用、系统、数据库/schema/表/字段、当前批次与修订。业务归属、应用、责任团队、组织、地域、共享对象、保留期限、业务说明和CMDB标识可人工确认，完整修订记录最多每次读取50项。

CMDB标准JSON示例：

```json
{"version":1,"source":"enterprise-cmdb","batchId":"20261111-001","records":[{"sourceRef":"order-app","parentRef":"","kind":"application","name":"订单应用","system":"commerce","metadata":{"business":"订单业务","ownerTeam":"研发团队","businessOrganization":"零售事业部","region":"中国","sharingRecipients":"内部财务","retention":"按已确认保留规则执行"}}]}
```

同工作区/source/batchId相同内容幂等重放，异内容冲突。`sourceRef`应是源系统稳定标识，不能依靠显示名称去重。为现有采集对象补充业务信息时增加其真实 `assetId`；跨工作区ID拒绝。已人工确认的不同属性进入conflicts，保留人工值。自动采集更新元数据时保留已导入业务属性和人工确认。目录最多展示1,000项并提示截断，支持类型与名称/路径搜索。

`CurrentCatalogFlowTargets`/事务内 `readCatalogFlowTargets` 只返回同工作区、启用来源、当前配置修订、最新collect成功批次且仍在新鲜度窗口内的真实表。窗口为计划周期3倍且至少60分钟。测试成功批次不替换目录；连接测试失败或失败collect、漏表、来源修订、暂停或超时过期会失去精确匹配。helper在调用方的repeatable-read事务中读取，报告与图可以保持同一数据库快照。

## 本地合成样本

`docker/compose.discovery-sample.yaml` 是独立Compose项目，在回环13316/13317/13318暴露MySQL/MongoDB/MariaDB。仅载入明确的合成样本，原平台和sample-db不受影响。密码使用被git忽略的env文件。

```sh
docker compose --env-file .env.scarlet-discovery -f docker/compose.discovery-sample.yaml up -d
```

Go测试使用客户环境变量 `SCARLET_MYSQL_TEST_DSN`、`SCARLET_MARIADB_TEST_DSN`、`SCARLET_MONGO_TEST_DSN`、`SCARLET_DISCOVERY_PG_TEST_DSN`；持久化测试用独立 `PLUME_STORE_TEST_DSN`。运行 `go test -race -p 1 ./internal/collector ./internal/store -run 'TestDiscovery|TestCatalogDiscovery' -count=1 -v`。不得把DSN明文提交到仓库。

官方依据：[Go MySQL driver](https://pkg.go.dev/github.com/go-sql-driver/mysql)、[MongoDB Go Driver](https://www.mongodb.com/docs/drivers/go/current/get-started/)、[OpenMetadata Table schema](https://github.com/open-metadata/OpenMetadata/blob/main/openmetadata-spec/src/main/resources/json/schema/entity/data/table.json)、[Lineage schema](https://github.com/open-metadata/OpenMetadata/blob/main/openmetadata-spec/src/main/resources/json/schema/type/entityLineage.json)、[Table API Resource](https://github.com/open-metadata/OpenMetadata/blob/main/openmetadata-service/src/main/java/org/openmetadata/service/resources/databases/TableResource.java)。

## 与数据流、检查和报告的绑定

多源真实库表进入数据流图；匹配键包含数据库系统、来源、数据库、schema与表，SQL Server/Oracle/DB2的标准系统别名归一，MySQL/MariaDB的数据库与同名schema视为同一命名空间。MySQL与MariaDB仍是不同系统；MongoDB使用空schema。源类型相同、别名相同但存在两个资产身份时标记ambiguous，不生成精确表边；不同系统同名不能误连。详情可核对字段候选、采用标准和有限采集范围。

每次成功collect保存不可变分类评估，含来源配置修订、运行、资产/人工分类修订、标准完整规则、客户识别规则、命中计数和缺条件，并为表创建G02检查。证据按同事实及检查周期去重。只有人工确认后重新领取任务得到的新的正向源端统计相容，且当前来源、规则、标准和采集范围仍有效时，才允许提交与独立复核。来源失败、漏表、配置修订、在途任务领取后的人工改判、规则替换或内容权限/读取缺口会使旧证据失效。手工登记的retest不能替代新来源复测；整改后须重新采集，自动生成相应retest。已验证检查后出现更高的数据属性冲突会创建关联上一检查的新周期，原人工判定与历史报告均保留。

工作区合规报告在同一repeatable-read快照中列出来源状态与检查采用的不可变目录评估。每项有check/evidence/assessment/source/run引用和当前有效标记；报告归档后随JSON冻结。来源未知权限不会被展示成通过权限审计；G02仅核验其可观察分类范围，不自动认定重要或核心数据。

同一个来源的provider不可修改，避免旧目录身份混用不同驱动。迁移到其他provider应新建来源，旧来源暂停后其精确关联即失效。

统一报告还冻结运行时发现采用的策略版本、实际统计窗口、请求/信号分母和缺口，策略后续编辑不会改写旧发现采用的策略。与列明检查关联的处置、审批/回执和来源安全事件，以及关联活动的最近专项评估引用分别列示；applied只是来源回执，不替代来源复测及独立复核。HTML导出对来源、规则、批注等动态文字进行转义，JSON保留结构化引用。

## 本地Docker持续采集

`compose.discovery-agents.yaml` 可叠加到根项目Compose中。先独立启动 `docker/compose.discovery-sample.yaml` 样例项目，再启动三个侧车：`discovery-mysql-agent`、`discovery-mariadb-agent`、`discovery-mongodb-agent`。它们同时加入根默认网络和既有外部网络 `scarlet-discovery-sample_default`，不新增数据库端口或改动数据库数据。应用镜像须包含 `scarlet-discovery-agent`。

将平台创建的三个任务令牌和客户侧连接字符串写入被忽略、权限受控的env文件，使用以下变量：`SCARLET_DISCOVERY_MYSQL_TOKEN` / `SCARLET_DISCOVERY_MYSQL_DSN`、`SCARLET_DISCOVERY_MARIADB_TOKEN` / `SCARLET_DISCOVERY_MARIADB_DSN`、`SCARLET_DISCOVERY_MONGODB_TOKEN` / `SCARLET_DISCOVERY_MONGODB_DSN`。MySQL/MariaDB的DSN使用 `mysql:3306` / `mariadb:3306` 和 `discovery_sample`；MongoDB使用 `mongodb:27017/discovery_sample`、`authSource=discovery_sample`。凭据不写进Compose文件。

显式 `-local-docker` 模式仅允许平台 `http://app:8080`、上述三个固定服务及 `sample-db:5432/scarlet_sample` 的PostgreSQL样例；OpenMetadata另仅允许固定 `http://openmetadata:8585[/api]`，绑定PostgreSQL类型、`scarlet_sample`数据库和`inventory`范围。拒绝任意私网、公网地址、其他端口、路径及数据库。该模式可用2秒轮询；生产仍要求验证TLS证书和主机名，原回环开发选项不扩展为任意私网明文。侧车设置 `restart: unless-stopped`，不依赖Mac终端会话持续存在。

真实OpenMetadata服务、官方CLI采集、独立读写账号及复现步骤见[OpenMetadata本地联调](openmetadata-live.md)。该实例目前提供元数据，内容识别和源ACL仍未采集；G02会记录缺口，不能仅凭元数据标签判定内容分类核验通过。真实两源联合验收见[验收记录](acceptance-live-integrations-2026-11-11.md)。

首页“已配置来源”按配置渠道计数：非撤销自动任务、运行时来源、全部发现任务（包括暂停）及非撤销运营来源。来源最多预览6项，显示最近记录状态；不表示实时连通、成功覆盖或去重物理系统数。

## CI 样例运行器兼容性

原生采集CI使用独立的Ubuntu22.04运行器验证三个真实数据库容器。首轮`ubuntu-latest`运行器中MongoDB8.0因内核兼容检查直接退出，未进入采集测试；依据[MongoDB上游问题](https://jira.mongodb.org/browse/SERVER-121912)和[GitHub运行器清单](https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2204-Readme.md)，暂用当前6.8内核运行器。此选择仅限该测试任务；上游版本修复与运行器退役前需再次验证迁移。
