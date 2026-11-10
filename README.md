# 赤翎 Scarlet Plume

面向中国企业的开源数据安全治理与合规运营工作台。按工作区登记数据资产和处理活动，将监管要求关联到具体检查，记录证据、整改和独立复核，并导出检查记录。

社区版以 Apache 2.0 开放自部署。仓库当前提供开发预览。

- 数据资产可分别记录业务敏感级、个人信息类别和法定重要／核心数据属性。
- 处理活动覆盖内部处理、对外共享或委托，以及 AI 使用。AI 活动可登记模型服务、处理地域、留存和训练条款。
- 检查单记录证据来源和脱敏摘要，支持提交、复核、整改复测和导出 JSON。检查创建人、提交人和证据作者需要回避复核。

## 产品目标

- 以真实任务比较现有产品。用同一范围测量部署接入、取证、整改复测和数据导出的成本与耗时，再据此决定功能优先级。
- 社区版保留核心治理流程和记录导出。企业可以自行部署、核查代码，并迁移自己的记录。
- 托管、实施和支持按服务分别计价，发布时公开计价单位、交付范围和超额费用。以同等交付范围下的总成本为比较口径。

## Go 与 Vue 开发预览

新栈采用 Go 1.27、Vue 3 / TypeScript、Node.js 26 LTS 和 PostgreSQL 18。已提供登录与工作区、资产、集成目录，以及 PostgreSQL 和 OpenMetadata 资产清单采集与导入。处理活动和控制证据流程目前可在下面的 Django 预览体验。 [架构与迁移顺序](docs/architecture.md)

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

采集器从客户环境读取源连接配置。在 `backend/` 下运行下面的本地示例，盘点指定 schema 后生成 JSON，使用授权工作区的「集成与导入」上传。生产源使用专用元数据账号，并移除回环明文选项。

```sh
export SCARLET_PG_DSN='postgresql://metadata_user@127.0.0.1:5432/inventory?sslmode=disable'
go run ./cmd/collector -provider postgresql -source CRM -schema public -allow-insecure-loopback > inventory.json
```

OpenMetadata 连接器通过 `SCARLET_OPENMETADATA_URL` 和 `SCARLET_OPENMETADATA_TOKEN` 接收客户侧配置，执行 `go run ./cmd/collector -provider openmetadata -source Catalog -service crm`。令牌由源系统的最小权限身份提供。清单只包含表名与源端标识。当前导入是增量发现，后续盘点缺项不会删除现有资产。

运行 `go test -race ./...` 时，可为 `PLUME_STORE_TEST_DSN` 配置专用的测试 PostgreSQL 数据库以执行持久化和授权集成测试。GitHub Actions 为两个开发栈分别启动 PostgreSQL 18。

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

当前版本由企业人工确认适用性和证据，设计上接收证据引用及脱敏摘要。外部源系统采集、权限执行、AI 实时拦截和监管报送由后续集成覆盖。内置条目提供官方依据，具体适用范围由企业确认。

## 许可证

项目代码使用 Apache License 2.0，详见 [LICENSE](LICENSE)。界面字体 Grove Sans SC 是思源黑体的重命名子集，按 SIL Open Font License 1.1 分发。 [字体许可证](governance/static/governance/fonts/OFL.txt)
