# 赤翎 Scarlet Plume

面向中国企业的开源数据安全治理与合规运营工作台。按工作区登记数据资产和处理活动，将监管要求关联到具体检查，记录证据、整改和独立复核，并导出合规检查报告。

社区版以 Apache 2.0 开放自部署。仓库当前提供开发预览。

- 多源发现与分类：原生PostgreSQL、MySQL、MariaDB、MongoDB采集，分类标准、客户关键词/RE2/词典规则、受限内容统计、业务目录和CMDB导入。
- 流转与调查：应用/API/真实库表图、历史结构差异、去标识主体调查及双端Span核验的跨服务关系；原值和完整SQL留在客户侧。
- 风险与运营：契约和运行时策略、经独立审批的受限API主动验证、外部安全事件、代理阻断与PostgreSQL直接SELECT撤销/回滚。
- 合规闭环：处理活动、PIA/出境/AI/业务变更问卷、采用版本、来源证据、整改复测、独立复核和冻结的HTML/JSON报告。

本轮能力、测试方式及边界见[红途材料独立实现映射](docs/hongtu-capability-mapping.md)。浏览器字段标注已支持用户主动授权采集、真实OpenAPI字段同名候选及人工确认快照；OpenMetadata2.0.7及Wazuh4.14.10已完成本地真实实例联调，目录元数据、原生规则告警、故障、整改复测及独立复核结果见[真实集成验收](docs/acceptance-live-integrations-2026-11-11.md)。四类原生数据库与受控PG/代理动作有真实合成样例验证，不能据此宣称所有数据库均有原生驱动、所有权限路径均已控制，或达到其他产品的性能与检测精度。

## 产品目标

- 以真实任务比较现有产品。用同一范围测量部署接入、取证、整改复测和数据导出的成本与耗时，再据此决定功能优先级。
- 社区版保留核心治理流程和记录导出。企业可以自行部署、核查代码，并迁移自己的记录。
- 托管、实施和支持按服务分别计价，发布时公开计价单位、交付范围和超额费用。以同等交付范围下的总成本为比较口径。

进入工作区默认显示“概览”。导航按数据治理与防护、国内合规与迎检、个人信息保护、数据跨境合规、AI数据安全、数据共享与委托、风险监测与处置七个场景组织；29个专项入口提供适用条件、检查要求、能力边界与报告参考。分类分级等技术结果直接归入对应场景，接入及规则配置集中在“专业工具与配置”；原19个模块路径及深链接继续兼容，顶部可按场景、检查项或功能名称查找。

场景页以技术检查为主：选定真实资产或活动，复用采集观察运行检查，逐项列出符合、不符合、待核实结果及整改建议。首批检查支持多源分类一致性和托管 PostgreSQL 的 PUBLIC 直接 SELECT 技术基线；覆盖不足的保护措施保持待核实。可执行的问题接入受控整改申请、独立审批、来源执行及新采集复查，原检查快照保持不变。[技术检查、整改与报告证据](docs/technical-scenario-checks.md)。

个人信息保护、国内合规与迎检、数据跨境合规已接通分类分级结果：按所选资产或活动自动带入可信分类、标准、目录、发布版本和已有业务事实，辅助证据与报告准备列出待补充及需核实内容。每次技术检查可导出HTML报告、JSON快照、CSV发现清单及带摘要清单的ZIP证据包；“监管报告要求”展示报告内容、技术证据覆盖、缺项和需另行完成的签章报送程序。新建专项评估自动采用可用分类上下文，旧评估与报告采用快照不被改写。[场景数据联动说明](docs/scenario-data-automation.md)。

## Go 与 Vue 开发预览

新栈采用 Go 1.27.3、Vue 3 / TypeScript、Node.js 26 LTS 和 PostgreSQL 18。工作区、资产、处理活动及检查沿用同一身份和证据链，监管检查保留Django基线的14条规则，并接入本轮发现、风险、运营及专项评估模块。 [架构与迁移顺序](docs/architecture.md)

### 本机 Docker

以下命令从项目根目录执行。Docker Compose 会构建 Go 与 Vue 镜像，在独立卷中运行 PostgreSQL 18。控制台默认通过 `http://127.0.0.1:8890` 访问。如果需要更换端口，在 `.env.scarlet-local` 设置 `SCARLET_LOCAL_PORT`。

```sh
umask 077
if [ ! -e .env.scarlet-local ]; then
  printf 'SCARLET_LOCAL_DB_PASSWORD=%s\n' "$(openssl rand -hex 32)" > .env.scarlet-local
fi

scarlet() { docker compose --env-file .env.scarlet-local -f compose.local.yaml "$@"; }
scarlet up -d --wait db
scarlet build app
scarlet run --rm -T app -local-docker -mode migrate
```

为负责人设置并导出 `SCARLET_BOOTSTRAP_PASSWORD`，然后初始化工作区。再次初始化保留账号原密码。

```sh
export SCARLET_BOOTSTRAP_USER=scarlet-owner
export SCARLET_BOOTSTRAP_WORKSPACE=local
export SCARLET_BOOTSTRAP_WORKSPACE_NAME=赤翎本地工作区
: "${SCARLET_BOOTSTRAP_PASSWORD:?请先设置并导出负责人密码}"
scarlet run --rm -T -e SCARLET_BOOTSTRAP_USER -e SCARLET_BOOTSTRAP_PASSWORD \
  -e SCARLET_BOOTSTRAP_WORKSPACE -e SCARLET_BOOTSTRAP_WORKSPACE_NAME app -local-docker -mode bootstrap
unset SCARLET_BOOTSTRAP_PASSWORD
scarlet up -d --wait app
```

打开 [本机赤翎控制台](http://127.0.0.1:8890)，用初始化的账号登录。控制台端口绑定本机回环地址，数据库通过 Compose 私有网络连接。运行 `scarlet stop` 可暂停，`scarlet up -d --wait` 可恢复。升级时重新构建镜像并运行迁移，再启动新版容器。

需要在本机体验 PostgreSQL 自动采集时，可叠加 `compose.sample.yaml` 启动独立样例库，再在工作台创建采集任务。使用样例的部署应在升级时继续加载两份 Compose 文件。 [样例库与托管采集步骤](docs/automation.md#在工作台管理-postgresql-采集)

### 可选的本地能力覆盖文件

基础控制台仍为[本机入口](http://127.0.0.1:8890)。新增模块先执行当前镜像的`-mode migrate`，再启动或更新服务与侧车；升级时保留正在使用的覆盖文件和现有数据卷，不通过清空数据库完成迁移。

| 文件 | 本地用途 | 配置说明 |
| --- | --- | --- |
| `compose.sample.yaml` | 独立PostgreSQL样例及托管采集 | [自动采集](docs/automation.md#在工作台管理-postgresql-采集) |
| `compose.runtime.yaml` | Java/Python HTTP及HTTPS观测样例 | [运行时接入](docs/runtime-security.md) |
| `compose.flow-sample.yaml`、`compose.service-sample.yaml` | 真实SQL查询与双服务父子链样例 | [数据流与调查](docs/data-flows.md) |
| `docker/compose.discovery-sample.yaml` | 独立Compose项目中的MySQL/MariaDB/MongoDB样例 | [原生发现样例](docs/discovery.md#本地合成样本) |
| `compose.discovery-agents.yaml` | 加入根默认网及上述既有外部样例网的三个持续采集侧车 | [持续发现与六个外部配置变量](docs/discovery.md#本地docker持续采集) |
| `compose.controls.yaml` | 已审批代理/PG处置的可选本地执行配置 | [安全运营与执行边界](docs/security-operations.md) |
| `compose.validation-sample.yaml` | 固定账号、合成对象与真实PG查询的API主动验证样例 | [批准范围及测试清单](docs/api-validation.md) |
| `compose.openmetadata.yaml` | 独立OpenMetadata实例、官方PG ingestion及目录采集侧车 | [真实目录部署](docs/openmetadata-live.md) |
| `compose.wazuh.yaml` | 根项目中的真实Wazuh manager、实际HTTP检查与事件桥接 | [真实事件与复测](docs/wazuh-live.md) |

这些文件不是全部直接合并为一个项目：发现样例库有独立项目和既有网络；其他覆盖文件按相应说明叠加到根项目。先创建被授权的工作区来源、绑定任务及令牌，再把凭据写入被忽略、权限受控的env文件。具体组合命令和本地来源配置以对应模块文档及实际部署配置为准；不要将示例地址或来源标识当作已创建的任务。

登录后默认进入概览，可从侧栏按数据治理、国内合规、个人信息保护、数据跨境、AI 数据安全、共享委托、风险处置等场景查看结果和运行检查。每个场景将“结果与发现”和“专项技术检查”分列，例如分类分级和数据流转直接位于数据治理场景；专业工具与配置集中展示数据接入、采集、规则及模板管理。原有页面路径继续兼容，共用页面保留进入时的场景。场景页可按已有资产或处理活动读取采集事实，并接入客户侧应用配置 API。[场景数据自动获取与接入](docs/scenario-data-automation.md)。数据流和风险统计标明来源窗口；首页来源数包含暂停的发现配置及未撤销集成，不等同实时在线率。

### 在宿主机启动 Go 与 Vue

先按下文的本地数据库步骤启动 PostgreSQL。示例连接串使用 `openssl rand -hex 32` 生成的本地数据库密码。生产连接使用 `sslmode=verify-full`。

```sh
set -a
. ./.env
set +a
docker compose up -d --wait db

export SCARLET_DB_DSN="postgresql://$APP_DB_USER:$APP_DB_PASSWORD@$APP_DB_HOST:$APP_DB_PORT/$APP_DB_NAME?sslmode=disable"
export SCARLET_ORIGIN=http://127.0.0.1:8080
export SCARLET_BOOTSTRAP_USER=demo-owner
export SCARLET_BOOTSTRAP_WORKSPACE=demo
export SCARLET_BOOTSTRAP_WORKSPACE_NAME=示例制造工作区
printf '为演示账号生成的密码：'
export SCARLET_BOOTSTRAP_PASSWORD="$(openssl rand -hex 16)"
printf '%s\n' "$SCARLET_BOOTSTRAP_PASSWORD"

npm ci --prefix web
npm run build --prefix web
cd backend
go run ./cmd/server -mode migrate -allow-insecure-loopback
go run ./cmd/server -mode bootstrap -allow-insecure-loopback
unset SCARLET_BOOTSTRAP_PASSWORD
go run ./cmd/server -web ../web/dist -allow-insecure-loopback
```

打开 [赤翎 Go 与 Vue 控制台](http://127.0.0.1:8080)，用首次初始化时生成的账号密码登录。再次执行初始化会保留已建立的账号。开发时也可用 `npm run dev --prefix web`，并将 Go API 的 `SCARLET_ORIGIN` 设为 `http://127.0.0.1:5173`。

工作台的「自动采集」支持 PostgreSQL、OpenMetadata 和飞书权限定期采集。托管 PostgreSQL 可采集结构、注释、约束、权限和源内有界格式统计，自动判定数据属性并保存采用的标准与事实，按任务配置生成 G02/G03 证据、整改复测和合规报告快照。 [自动采集、检查与报告](docs/automation.md)

「API 安全」可扫描 OpenAPI 3.0 或 3.1 JSON，自动登记接口资产，将鉴权与疑似敏感字段等契约风险接入检查和取证流程，并导出扫描结果。 [API 安全与能力对照](docs/api-security.md)、[API 运行时观测与 HTTPS 接入](docs/runtime-security.md)

“多源自动发现”支持客户侧计划、连接测试、立即采集与令牌轮换；“业务资产目录”保留自动来源、人工确认、CMDB冲突和修订历史。分类采用标准与识别规则版本，人工确认后须取得新的相容正向来源事实才能用于G02复核。[发现、规则与目录](docs/discovery.md)

“API生命周期”对照契约版本与运行时观测；“数据流转”汇总应用、接口、去标识主体和当前真实库表，支持结构快照、差异及来源核验的跨服务Span关系。客户侧JSON识别只上报类型统计。[数据流与本地真实查询样例](docs/data-flows.md)

“运营与处置”接收过滤后的安全事件，跟踪风险、独立审批、实际执行及回滚回执。PG权限动作仅处理已确认的直接表级SELECT，不覆盖所有者、继承或列级等其他路径。“API主动验证”使用客户侧批准清单，对固定GET目标执行双账号对象授权和显式弱测试凭据验证，结果进入原生G03整改复核链；无任意公网扫描、字典或爆破。[安全运营](docs/security-operations.md)、[API主动验证](docs/api-validation.md)

离线清单导入仍可使用。采集器从客户环境读取源连接配置。在 `backend/` 下运行下面的本地示例，盘点指定 schema 后生成 JSON，使用授权工作区的「集成与导入」上传。生产源使用专用元数据账号，并移除回环明文选项。

```sh
export SCARLET_PG_DSN='postgresql://metadata_user@127.0.0.1:5432/inventory?sslmode=disable'
go run ./cmd/collector -provider postgresql -source CRM -schema public -allow-insecure-loopback > inventory.json
```

OpenMetadata 连接器通过 `SCARLET_OPENMETADATA_URL` 和 `SCARLET_OPENMETADATA_TOKEN` 接收客户侧配置，执行 `go run ./cmd/collector -provider openmetadata -source Catalog -service crm`。令牌由源系统的最小权限身份提供。这条旧离线命令的清单仅包含表名与源端标识；新“多源自动发现”任务使用深目录适配，另读schema、字段、注释、标签、约束与可得血缘。两种入口的覆盖不同，源权限缺项保留unknown。当前导入是增量发现，盘点缺项不会直接删除资产，但当前目录/证据有效性会按对应采集规则失效。

「分类分级」支持真实资产台账、分类与分级分布、业务分类目录、批量确认和独立评审发布，并提供冻结快照的 CSV/JSON 导出。工作区默认标准及资产逐项选标保存所用等级、条款和取证要求的版本快照。覆盖范围、官方来源及待补条款见 [分类分级标准库](docs/classification-standards.md)。

自有规则识别引擎默认本地运行；「识别引擎与 AI」可由用户自选 HTTPS 模型服务、Chat Completions/Responses 兼容协议及手填模型标识，加密保存密钥。显式授权并预览所选资产的受限元数据后，混合模式允许按次生成 AI 参考建议，仍由人工确认采用。具体模型由部署方配置，两种协议已有隔离 HTTP/TLS 验证；知识检索、本地训练和运营参考分别保留采用范围及人工决策。配置、接口与范围说明见[分类分级工作台](docs/classification-workbench.md)。

### 文件、指纹及识别增强

本地文件Agent支持有界文本、CSV、JSON/JSONL、DOCX/PPTX识别；PDF及图片OCR需在采集机显式配置Poppler/Tesseract。只上传匿名文件引用、命中统计和覆盖，不上传正文。源端HMAC指纹支持文件/运行时等值调查；加解密转换SDK需要客户明确接入，保留同源审计及两侧指纹关系，不通用解密。[文件与指纹说明](docs/file-content-and-value-evidence.md)。

分类知识采用本地词项检索，逐次授权后将选定摘录交给自选模型。本地监督模型支持显式训练/评估划分、指标及版本化参考预测。独立发布快照及安全事件可由绑定客户侧Agent投递至明确接收方，接收回执与第三方内部应用分别记录。[知识、训练与交付](docs/classification-intelligence-and-delivery.md)。

两份参考手册的逐页实现、证据与边界见[15+33页能力矩阵](docs/reference-scenarios-15-and-33.md)。

### 处理活动、历史数据与监管检查

对外共享、数据出境和 AI 使用均可从活动详情创建关联检查。证据、整改复测和独立复核沿用同一工作流，JSON 导出包含对应的活动范围。 [场景操作与验收示例](docs/activities.md)

「检查模板」内置对外共享、数据出境和 AI 使用三套模板，共 11 项。工作区负责人可配置适用条件、取证要求、验收标准并发布新版本。已有检查保留原版本。「合规报告」可按工作区或活动导出 HTML 和 JSON。 [检查模板与报告说明](docs/templates-reports.md)

“合规评估”提供PIA、出境、AI使用、业务变更四类版本化问卷，保留采用条款、活动和机器事实、答复、证据引用、缺口与独立复核；活动或来源变化触发待重评。专项报告独立导出，合规报告仅冻结相关活动的评估引用和状态，不把问卷完整性校验等同实质法律结论。[专项评估说明](docs/assessments.md)

报告还冻结目录不可变评估、运行时命中时策略与实际窗口/分母、关联处置与来源回执、整改及独立复核。`applied`为执行来源回执，不自动认定检查“已验证”；客户样本和原始日志不进入这些报告。

原 Django 用户可使用一次性命令将工作区、资产分级、处理活动、检查、证据及审计事件导入同一 PostgreSQL 数据库的 `plume` schema。命令默认只预检，正式导入使用 `-apply`，迁入账号默认停用并按需设置新密码。 [Django 迁移手册](docs/django-migration.md)

升级现有 Go 预览时，先执行 `go run ./cmd/server -mode migrate -allow-insecure-loopback`。该命令更新 Go 数据库结构。迁入 Django 历史数据时，按迁移手册另行执行导入。

从 `backend/` 为工作区创建独立复核账号。请在本机为 `SCARLET_REVIEWER_PASSWORD` 设置单独的密码，并在执行后清除环境变量。重复执行会保留已有复核账号密码。

```sh
export SCARLET_REVIEWER_USER=demo-reviewer
go run ./cmd/server -mode add-reviewer -allow-insecure-loopback
unset SCARLET_REVIEWER_PASSWORD
```

经办人可以在「监管检查」关联资产与规则、登记脱敏证据并提交。创建人、当前提交人和证据登记人都需要回避复核。飞书 G03 检查可绑定指定的直接协作者，要求整改后登记整改记录、重新采集同一目标，再由独立复核人核对。采集器通过模拟飞书接口测试，Go、PostgreSQL 与 Vue 通过合成清单跑通完整流程，真实飞书租户联调待进行。 [联调步骤与验收口径](docs/feishu.md)

运行 `go test -race -p 1 ./...` 时，为 `PLUME_STORE_TEST_DSN` 配置可丢弃的独立PostgreSQL18/SCRAM测试库，以执行持久化和授权集成测试。各包共享全局采集队列，因此按包串行，仍启用竞态检测。原生数据库、PG权限执行、HTTP/HTTPS代理、跨服务链路、主动验证、问卷与报告的真实/协议测试边界分别见模块文档；没有对应环境变量的集成测试会跳过，不能把跳过计为已验收。GitHub Actions验证两个开发栈，并使用独立样例测试Django历史导入。

## Django 基线本地运行

需要 Python 3.14 和 Docker Compose。开发、测试和生产均使用 PostgreSQL。本地 Compose 启动 PostgreSQL 18，默认只监听本机的 `127.0.0.1:5432`。

首次运行时，在项目根目录创建本地配置并生成数据库密码。将生成的值填入 `.env` 的 `APP_DB_PASSWORD`，保留 `.env.example` 中的空值。

```sh
cp .env.example .env
chmod 600 .env
openssl rand -hex 32
```

在同一个终端加载配置，启动数据库，再初始化工作区。为两个应用账号分别设置密码，用来体验提交和独立复核。

```sh
set -a
. ./.env
set +a
docker compose up -d --wait db

python3.14 -m venv .venv
source .venv/bin/activate
python -m pip install -r requirements.txt

python manage.py migrate
python manage.py provision_workspace demo "演示工作区" demo-owner --role owner
python manage.py provision_workspace demo "演示工作区" demo-reviewer --role reviewer
python manage.py seed_demo demo
python manage.py runserver
```

新账号的密码按命令提示设置，自动化创建新账号时可通过环境变量 `APP_PROVISION_PASSWORD` 注入密码。`seed_demo` 为尚无业务数据的工作区写入合成样本。 [打开本地登录页](http://127.0.0.1:8000/login/)

本地数据库使用 Docker 数据卷。若本机端口已被占用，修改 `.env` 中的 `APP_DB_PORT`，再加载配置并启动。已有数据库的数据卷沿用初始化时设置的账号密码。早期预览的演示数据可以在新库重新生成。有需保留的 SQLite 数据时，先保留原文件，再单独规划导入和核验。

## 验证

在已激活虚拟环境、加载 `.env` 且 PostgreSQL 已启动的终端运行以下命令。

```sh
python manage.py makemigrations --check --dry-run
python manage.py test
```

GitHub Actions 使用临时 PostgreSQL 服务验证迁移和测试。Django 测试会创建独立的测试库，因此运行测试的本地账号需要创建数据库的权限。 [Django 测试数据库说明](https://docs.djangoproject.com/en/5.2/topics/testing/overview/#the-test-database)

## 生产配置与范围

未设置 `APP_ENV` 时按生产模式启动。生产建议使用私网可达的托管 PostgreSQL 18，及时更新小版本，并启用备份及时间点恢复。生产模式要求显式允许的主机列表和独立密钥，启用 HTTPS 跳转、HSTS 及安全会话和 CSRF Cookie。仓库中的 Compose 与 Django 开发服务器用于本地开发。 [PostgreSQL 版本支持](https://www.postgresql.org/support/versioning/)

| 环境变量 | 用途 |
| --- | --- |
| `APP_SECRET_KEY` | 由部署环境注入的随机密钥，至少 50 个字符 |
| `APP_ALLOWED_HOSTS` | 逗号分隔的明确域名或地址 |
| `APP_DB_NAME`、`APP_DB_USER`、`APP_DB_PASSWORD`、`APP_DB_HOST` | PostgreSQL 连接信息。主机应匹配服务端证书 |
| `APP_DB_PORT` | PostgreSQL 端口，默认 `5432` |
| `APP_DB_SSLROOTCERT` | PostgreSQL CA 文件路径，未设置时使用客户端默认 CA 位置 |
| `APP_TRUST_PROXY_HTTPS` | 仅在受信代理剥离客户端同名头并覆盖 `X-Forwarded-Proto` 时设为 `1` |

数据库连接使用 `verify-full` 校验证书链和主机名。使用托管服务的数据库域名，并将其 CA 文件挂载到应用和迁移任务可读的位置。通过部署环境的密钥管理服务分别注入应用密钥和数据库凭据。 [PostgreSQL TLS 校验](https://www.postgresql.org/docs/18/libpq-ssl.html)

为数据库准备两个角色。迁移角色可在应用 schema 中创建对象并拥有这些对象，用于发布时执行 `python manage.py migrate`。运行角色仅获得连接、schema 使用以及业务所需的表和序列权限。对迁移角色日后创建的对象配置对应的默认授权。发布任务和应用服务各自注入所属角色的 `APP_DB_USER` 和 `APP_DB_PASSWORD`。 [PostgreSQL 默认权限](https://www.postgresql.org/docs/18/sql-alterdefaultprivileges.html)

生产环境使用 WSGI 服务加载 `config.wsgi:application`，通过部署平台提供 TLS 和静态文件服务。上线前运行 `python manage.py collectstatic --noinput` 和 `python manage.py check --deploy`，并配置登录限速、监控和恢复演练。

当前版本接收源系统引用和脱敏证据。已接入的目录和权限信息可定期采集。企业负责确认规则适用范围、执行授权整改和独立复核。

## 许可证

项目代码使用 Apache License 2.0，详见 [LICENSE](LICENSE)。界面字体 Grove Sans SC 是思源黑体的重命名子集，按 SIL Open Font License 1.1 分发。 [字体许可证](governance/static/governance/fonts/OFL.txt)

本轮非浏览器能力覆盖的真实Docker场景、回归和兼容范围见[验收记录](docs/acceptance-hongtu-coverage-2026-11-11.md)。

本轮新增九类运行时规则已接通“登记整改或成功执行回执 → 同范围完整新窗口自动复测 → 系统提交独立复核”。来源沉默、未知捕获、策略/来源失效或缩小主体范围不能通过，原经办人不能独立核验。规则版本变化后须取得修订后的新整改与新窗口。

数据外发与事件调查支持来源绑定的脱敏终端/数据库/AI上下文、Wazuh FIM与pgAudit投影、源端受控文本复制、AI出口转发前检查、冻结调查和可选模型运营参考。正常活动审计与风险发现分别处理，不把允许操作堆入整改待办。[数据安全事件与调查](docs/data-security-events-and-cases.md)、[全知与亿格云能力映射](docs/quanzhi-eagleyun-capability-mapping.md)。

### USB、打印与剪贴板

数据外发与事件调查已提供终端设备、临时通道策略、独立审批、源端加载回执及实际操作证据。Windows执行器区分原生观察与显式受控动作，不把加载等同全系统拦截；下载与接入命令见[终端通道说明](docs/endpoint-channel-enforcement.md)。

Windows企业终端另提供[部署与验收包源文件](endpoint/windows-enterprise/README.md)及[原生控制契约](docs/windows-enterprise-native-controls.md)：固定的可移动存储拒写、本地指定打印队列暂停、RDP剪贴板重定向限制，使用独立批准、限时租约、分阶段证据与保守恢复。Docker构建生成x64/ARM64执行器、ZIP和独立SHA256清单，下载入口位于终端设备页；Windows实机与PowerShell/SCM运行验收仍待完成，配置回读不代表功能阻断。
