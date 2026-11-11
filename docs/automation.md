# 自动采集、检查与报告

在「自动采集」创建任务后，由源系统所在环境中的赤翎采集器定期读源、脱敏并提交结果。工作区负责人可创建或撤销任务。平台只显示一次采集令牌，并在数据库保存令牌摘要。任务只可提交绑定来源的资产，或指定检查的飞书权限观测。

| 连接器 | 自动获得的内容 | 写入位置 |
| --- | --- | --- |
| PostgreSQL | 表、字段、注释、约束、估计规模、行级安全、直接 ACL 和源内有界格式统计 | 资产、自动分类及历史。托管任务可自动生成 G02/G03 检查、规则证据与报告归档 |
| OpenMetadata | 指定服务内的数据库、表目录及源端标识 | 资产台账和导入记录 |
| 飞书云文档 | 指定文档内一个直接协作者的权限状态 | 关联的 G03 检查证据和运行记录 |

源系统凭据在采集端配置。原始文档、协作者列表、表内记录和应用密钥留在源端。资产按工作区、提供方、来源别名和源端标识去重，后续清单的缺项保留为未观察到。飞书整改登记后，采集器会自动提交同一目标的新权限观测。已提交或已验证的检查保留证据历史，新的权限观测仍可在任务记录查看。复核期间检测到较新且仍有直接授权的观测会阻止通过，撤权和独立复核由具备权限的人员执行。飞书的观测范围是指定协作者的直接授权。 [飞书协作者权限接口](https://open.feishu.cn/document/server-docs/docs/permission/permission-member/list)

## 在工作台管理 PostgreSQL 采集

启用了托管采集的实例中，负责人可选择「新建 PostgreSQL 采集」，填写主机、端口、数据库、只读用户、密码及 schema。先点「测试连接」查看可采集表的数量，再保存。频率可设为 1 至 1,440 分钟。暂停后仍可点击「立即采集」。在任务行可查看最近结果、下一次运行和历史。需要完整流程时，在任务表单中勾选「自动检查与取证」和「自动归档报告」。已有任务可进入编辑开启这两项。

密码加密保存在平台数据库，编辑时留空可继续使用已保存的密码。修改主机、端口、数据库或用户时，需重新填写密码。部署端通过 `SCARLET_MANAGED_PG_KEY` 设置 32 字节随机密钥的十六进制表示，并用逗号分隔的 `SCARLET_MANAGED_PG_HOSTS` 配置允许的主机。请保存原密钥以便重启和升级后解密已有任务。正式连接使用 TLS 主机名校验。

本机 Docker 样例使用隔离网络中的 `sample-db:5432`。从仓库根目录执行，保留 `.env.scarlet-local` 中已有的数据库密码，以下配置仅在首次启用样例时加入。

```sh
umask 077
for key in SCARLET_MANAGED_PG_KEY SCARLET_SAMPLE_DB_PASSWORD SCARLET_SAMPLE_READER_PASSWORD; do
  if ! grep -q "^$key=" .env.scarlet-local; then
    printf '%s=%s\n' "$key" "$(openssl rand -hex 32)" >> .env.scarlet-local
  fi
done

scarlet() { docker compose --env-file .env.scarlet-local -f compose.local.yaml -f compose.sample.yaml "$@"; }
scarlet build app sample-db
scarlet up -d --wait db sample-db
scarlet run --rm -T app -local-docker -mode migrate
scarlet up -d --wait app
```

新建任务时，主机填写 `sample-db`，数据库填写 `scarlet_sample`，用户填写 `sample_reader`，schema 填写 `inventory`。密码使用本机 `.env.scarlet-local` 中的 `SCARLET_SAMPLE_READER_PASSWORD`。样例采集账号具有样例表的 schema 访问与 SELECT 权限，可以读取目录并在源端聚合五张合成表。样例库与平台库使用独立卷，两个数据库端口均留在 Docker 内。

## PostgreSQL 字段、权限与自动证据

平台托管任务采集字段名、注释、类型、顺序、可空性、表所有者、约束、优化器估计行数、行级安全状态，以及表级和列级直接 ACL。字段和注释用于标记疑似邮箱、电话号码、身份标识、金融账号、凭据、姓名、地址或 IP 地址。取得 SELECT 权限后，采集器还会在源端对可见样本运行格式统计，向平台返回命中计数。管理员可在资产的「字段与权限」和分类详情查看来源、批次及观察范围。

内容聚合每次最多覆盖 10 张表、每表 16 列、200 行，每表执行上限 500ms，支持文本、字符、整数及 numeric 字段。超出范围、权限受限、RLS 过滤、类型不支持或超时均有明确状态。身份证检查包含校验位，卡号检查包含 Luhn；统计单位是观察样本中的匹配行数。完整规则边界见 [自动分类与标准依据](classification-standards.md#自动化与确认边界)。

权限视图分别显示表级或列级 `PUBLIC SELECT`、schema 的 `PUBLIC USAGE`、指定被授权角色的 schema `USAGE`。实际有效访问还涉及角色成员关系、行级安全策略和连接资格，需要结合源端核验。疑似敏感标签供人工确认。 [PostgreSQL 权限](https://www.postgresql.org/docs/18/ddl-priv.html) · [目录权限查询函数](https://www.postgresql.org/docs/18/functions-info.html#FUNCTIONS-ACLITEM)

开启「自动检查与取证」后，平台按本次观察到的表自动创建或复用 G02 和 G03 检查。G02 保存所采用标准、逐条规则、格式统计、分类差异和采证缺口。G03 检查表级或列级的 PUBLIC 和具名非所有者直接 SELECT 授权。首次获得充分格式统计的空白资产会自动写入属性分类与来源历史，人工确认记录会形成差异比对。工作区负责人或经办人也可在已有 G02/G03 检查中选择「关联 PostgreSQL 采集任务」。

平台在待取证检查中自动登记来源观测，包含时间、任务和批次。整改登记后，下一次符合时间条件的同源采集可补充复测观测。同一轮的相同事实会去重。G02 的复测及独立复核会核对当前分类版本、标准、来源目标及最新事实；证据过期或存在差异时需要重新取证。已验证后再观察到分类差异会创建新的 G02 检查。每条证据最多列出 20 个识别字段和 20 条非所有者 SELECT 授权，超出部分以数量标示，规则事实与资产详情可以查看具体来源。

目录采集上限为 1,000 张表、10,000 个字段、20,000 条 ACL。超限时运行记录显示失败，已有资产和证据保留。样例库的 `inventory.customers` 保留字段与列级权限样例，`inventory.classification_demo` 在匿名字段中放置合成邮箱、虚构证件、电话格式和支付测试号，用于内容识别及整改复测。`inventory.classification_contacts` 提供保持自动来源状态的联系信息样例，可在分类台账筛选「自动识别」。

已有样例卷升级时，在完成应用迁移后应用新的合成样例脚本，再到工作台立即采集。新建样例卷会在初始化时自动执行该脚本。

```sh
scarlet exec -T sample-db psql -X -v ON_ERROR_STOP=1 --single-transaction \
  -U sample_admin -d scarlet_sample < docker/postgresql-sample/classification.sql
```

## 自动归档报告

开启「自动归档报告」后，每次成功的托管 PostgreSQL 目录采集会尝试保存一次报告快照。打开「合规报告」中的「自动报告归档」，可按批次查看采集时间、识别统计、与来源关联的检查、本次自动判定、证据、整改复测和独立复核记录，并导出 HTML 或 JSON。历史快照保留归档时的检查状态，后续采集或复核不会改写它。本次自动判定和独立复核状态分开显示。

单份归档最多包含 200 项检查、3,000 条证据与操作记录，以及 4 MiB 的 JSON 快照。超限或生成失败会显示相应的归档状态，已采集入库的资产和证据保留。每个任务保留最近 30 次归档结果，工作区列表展示最近 100 条。需要保留更久的报告可下载留存。

用本机样例验收时，勾选两个自动化选项后点「立即采集」。任务显示成功后，在资产详情核对字段和 ACL，在监管检查核对 G02/G03 判定及证据，再在自动归档列表核对同一采集批次并下载报告。重复采集相同目录时可以看到新的报告批次，资产和相同事实的证据保持去重。

API 的契约识别、资产登记和检查取证使用单独的「API 安全」入口。 [API 安全扫描与验收](api-security.md)

## 配置客户侧采集器

1. 在控制台选择连接器并创建任务。资产任务的来源别名应与下文 `-source` 一致。飞书任务从已绑定飞书目标的 G03 检查进入。
2. 在采集机通过受控环境变量设置 `SCARLET_AGENT_TOKEN` 以及所需源凭据。工作台的一次性令牌用于采集任务，源凭据无需输入工作台。
3. 将 `SCARLET_AGENT_URL` 设置为平台的 HTTPS 地址。与本机 Docker 控制台同一台机器做本地验证时，可使用 `http://127.0.0.1:8890` 并加 `-allow-insecure-loopback`。保持源端与平台时钟同步。
4. 在 `backend/` 运行相应命令。默认间隔 15 分钟，支持 `-interval` 调整。加 `-once` 可验证一次。需要持续采集时，将同一命令交给客户侧进程管理器。

```sh
# PostgreSQL，使用 SCARLET_PG_DSN 和只读元数据账号
go run ./cmd/agent -provider postgresql -source '生产数据库目录' -schema inventory -interval 15m

# OpenMetadata，使用 SCARLET_OPENMETADATA_URL 和 SCARLET_OPENMETADATA_TOKEN
go run ./cmd/agent -provider openmetadata -source '数据目录' -service crm -interval 15m

# 飞书，使用下文的应用和目标配置
go run ./cmd/agent -provider feishu -interval 15m
```

飞书定期采集配置 `SCARLET_FEISHU_APP_ID`、`SCARLET_FEISHU_APP_SECRET`、`SCARLET_FEISHU_DOC_TOKEN` 和 `SCARLET_FEISHU_MEMBER_OPEN_ID`。自建应用需要已获批的协作者列表读取权限，并获准访问目标文档。采集器在令牌即将过期时自动续期。验证一次时可通过 `SCARLET_FEISHU_ACCESS_TOKEN` 提供短期访问令牌并使用 `-once`。真实租户联调仍需飞书测试环境。 [飞书应用访问令牌接口](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/authentication-management/access-token/tenant_access_token) · [飞书目标绑定与验证](feishu.md)

## 查看结果

「自动采集」显示任务最近运行和结果。源端采集失败且平台可达时，采集器会提交简化的失败代码。平台暂不可达时，采集器会在本地报错并重试，同一次投递沿用原运行编号。成功、跳过和失败都可在运行历史查看。撤销任务令牌后，采集端下一次提交会被拒绝，已有运行及证据保留。
