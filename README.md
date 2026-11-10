# 赤翎 Scarlet Plume

面向中国企业的开源数据安全治理与合规运营工作台。按工作区登记数据资产和处理活动，将监管要求关联到具体检查，记录证据、整改和独立复核，并导出检查记录。

社区版以 Apache 2.0 开放自部署。未来如推出托管、实施或支持服务，分别列明计价单位、交付范围和额外费用。仓库当前提供开发预览。

- 数据资产可分别记录业务敏感级、个人信息类别和法定重要／核心数据属性。
- 处理活动覆盖内部处理、对外共享或委托，以及 AI 使用。AI 活动可登记模型服务、处理地域、留存和训练条款。
- 检查单记录证据来源和脱敏摘要，支持提交、复核、整改复测和导出 JSON。检查创建人、提交人和证据作者需要回避复核。

## 本地运行

需要 Python 3.13。在项目根目录执行以下命令。为两个账户设置不同的密码，分别用来提交和复核。

```sh
python3.13 -m venv .venv
source .venv/bin/activate
python -m pip install -r requirements.txt
export APP_ENV=development

python manage.py migrate
python manage.py provision_workspace demo "演示工作区" demo-owner --role owner
python manage.py provision_workspace demo "演示工作区" demo-reviewer --role reviewer
python manage.py seed_demo demo
python manage.py runserver
```

访问本地登录页。新账号的密码按命令提示设置，自动化创建新账号时可以通过环境变量 `APP_PROVISION_PASSWORD` 注入密码。命令不会重置已有账号的密码。`seed_demo` 为尚无业务数据的工作区写入合成样本。SQLite 默认保存在项目根目录的 `db.sqlite3`，可通过 `APP_SQLITE_PATH` 指定其它位置。 [打开本地登录页](http://127.0.0.1:8000/login/)

## 验证

```sh
APP_ENV=development python manage.py makemigrations --check --dry-run
APP_ENV=development python manage.py test
```

GitHub Actions 分别在 SQLite 和临时 PostgreSQL 服务上验证迁移和测试。PostgreSQL 测试配置只连接本机的临时数据库，生产模式仍要求 TLS 证书校验。

## 生产配置与范围

未设置 `APP_ENV` 时按生产模式启动。生产模式要求 PostgreSQL、显式允许的主机列表和独立密钥，启用 HTTPS 跳转、HSTS 及安全会话和 CSRF Cookie。

| 环境变量 | 用途 |
| --- | --- |
| `APP_SECRET_KEY` | 由部署环境注入的随机密钥，至少 50 个字符 |
| `APP_ALLOWED_HOSTS` | 逗号分隔的明确域名或地址 |
| `APP_DB_NAME`、`APP_DB_USER`、`APP_DB_PASSWORD`、`APP_DB_HOST` | PostgreSQL 连接信息。主机应匹配服务端证书 |
| `APP_DB_PORT` | PostgreSQL 端口，默认 `5432` |
| `APP_DB_SSLROOTCERT` | PostgreSQL CA 文件路径，未设置时使用客户端默认 CA 位置。连接采用 `verify-full` |
| `APP_TRUST_PROXY_HTTPS` | 仅在受信代理剥离客户端同名头并覆盖 `X-Forwarded-Proto` 时设为 `1` |

生产环境使用 WSGI 服务加载 `config.wsgi:application`，通过部署平台提供 TLS 和静态文件服务。上线前执行 `python manage.py migrate`、`python manage.py collectstatic --noinput` 和 `python manage.py check --deploy`，并配置登录限速、监控、备份及恢复演练。SQLite 和开发服务器仅用于本地开发。

当前版本由企业人工确认适用性和证据，设计上接收证据引用及脱敏摘要。外部源系统采集、权限执行、AI 实时拦截和监管报送由后续集成覆盖。内置条目提供官方依据，具体适用范围由企业确认。

## 许可证

项目代码使用 Apache License 2.0，详见 [LICENSE](LICENSE)。界面字体 Grove Sans SC 是思源黑体的重命名子集，按 SIL Open Font License 1.1 分发。 [字体许可证](governance/static/governance/fonts/OFL.txt)
