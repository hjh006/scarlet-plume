# 赤翎 Scarlet Plume

面向中国企业的开源数据安全治理与合规运营工作台。按工作区登记数据资产和处理活动，将监管要求关联到具体检查，记录证据、整改和独立复核，并导出合规检查报告。

社区版以 Apache 2.0 开放自部署。仓库当前提供开发预览。

- 数据资产可分别记录业务敏感级、个人信息类别和法定重要／核心数据属性。
- 处理活动覆盖内部处理、对外共享或委托、数据出境及 AI 使用。AI 活动可登记模型服务、处理地域、留存和训练条款。
- 检查单记录证据来源和脱敏摘要，支持提交、复核、整改复测和导出 JSON。检查创建人、提交人和证据作者需要回避复核。

## 产品目标

- 以真实任务比较现有产品。用同一范围测量部署接入、取证、整改复测和数据导出的成本与耗时，再据此决定功能优先级。
- 社区版保留核心治理流程和记录导出。企业可以自行部署、核查代码，并迁移自己的记录。
- 托管、实施和支持按服务分别计价，发布时公开计价单位、交付范围和超额费用。以同等交付范围下的总成本为比较口径。

## Go 与 Vue 开发预览

新栈采用 Go 1.27、Vue 3 / TypeScript、Node.js 26 LTS 和 PostgreSQL 18。已提供工作区、资产清单、处理活动、监管检查、证据登记、整改复测、独立复核和 JSON 导出。检查沿用 Django 基线的 14 条规则。 [架构与迁移顺序](docs/architecture.md)

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

工作台的「自动采集」支持 PostgreSQL、OpenMetadata 和飞书权限定期采集。平台托管的 PostgreSQL 任务可自动识别疑似敏感字段、采集直接 ACL，并按任务配置生成 G02/G03 检查证据和合规报告快照。 [自动采集、检查与报告](docs/automation.md)

「API 安全」可扫描 OpenAPI 3.0 或 3.1 JSON，自动登记接口资产，将鉴权与疑似敏感字段等契约风险接入检查和取证流程，并导出扫描结果。 [API 安全与能力对照](docs/api-security.md)、[API 运行时观测与 HTTPS 接入](docs/runtime-security.md)

离线清单导入仍可使用。采集器从客户环境读取源连接配置。在 `backend/` 下运行下面的本地示例，盘点指定 schema 后生成 JSON，使用授权工作区的「集成与导入」上传。生产源使用专用元数据账号，并移除回环明文选项。

```sh
export SCARLET_PG_DSN='postgresql://metadata_user@127.0.0.1:5432/inventory?sslmode=disable'
go run ./cmd/collector -provider postgresql -source CRM -schema public -allow-insecure-loopback > inventory.json
```

OpenMetadata 连接器通过 `SCARLET_OPENMETADATA_URL` 和 `SCARLET_OPENMETADATA_TOKEN` 接收客户侧配置，执行 `go run ./cmd/collector -provider openmetadata -source Catalog -service crm`。令牌由源系统的最小权限身份提供。清单只包含表名与源端标识。当前导入是增量发现，后续盘点缺项不会删除现有资产。

「分类分级」支持工作区默认标准及资产逐项选标，并保存所用等级、条款和取证要求的版本快照。覆盖范围、官方来源及待补条款见 [分类分级标准库](docs/classification-standards.md)。

### 处理活动、历史数据与监管检查

对外共享、数据出境和 AI 使用均可从活动详情创建关联检查。证据、整改复测和独立复核沿用同一工作流，JSON 导出包含对应的活动范围。 [场景操作与验收示例](docs/activities.md)

「检查模板」内置对外共享、数据出境和 AI 使用三套模板，共 11 项。工作区负责人可配置适用条件、取证要求、验收标准并发布新版本。已有检查保留原版本。「合规报告」可按工作区或活动导出 HTML 和 JSON。 [检查模板与报告说明](docs/templates-reports.md)

原 Django 用户可使用一次性命令将工作区、资产分级、处理活动、检查、证据及审计事件导入同一 PostgreSQL 数据库的 `plume` schema。命令默认只预检，正式导入使用 `-apply`，迁入账号默认停用并按需设置新密码。 [Django 迁移手册](docs/django-migration.md)

升级现有 Go 预览时，先执行 `go run ./cmd/server -mode migrate -allow-insecure-loopback`。该命令更新 Go 数据库结构。迁入 Django 历史数据时，按迁移手册另行执行导入。

从 `backend/` 为工作区创建独立复核账号。请在本机为 `SCARLET_REVIEWER_PASSWORD` 设置单独的密码，并在执行后清除环境变量。重复执行会保留已有复核账号密码。

```sh
export SCARLET_REVIEWER_USER=demo-reviewer
go run ./cmd/server -mode add-reviewer -allow-insecure-loopback
unset SCARLET_REVIEWER_PASSWORD
```

经办人可以在「监管检查」关联资产与规则、登记脱敏证据并提交。创建人、当前提交人和证据登记人都需要回避复核。飞书 G03 检查可绑定指定的直接协作者，要求整改后登记整改记录、重新采集同一目标，再由独立复核人核对。采集器通过模拟飞书接口测试，Go、PostgreSQL 与 Vue 通过合成清单跑通完整流程，真实飞书租户联调待进行。 [联调步骤与验收口径](docs/feishu.md)

运行 `go test -race ./...` 时，可为 `PLUME_STORE_TEST_DSN` 配置专用的测试 PostgreSQL 数据库以执行持久化和授权集成测试。GitHub Actions 在 PostgreSQL 18 验证两个开发栈，并使用由 Django 演示命令生成的独立数据库测试历史导入。

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
