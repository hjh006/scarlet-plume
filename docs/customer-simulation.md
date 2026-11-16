# 本地客户应用模拟

本示例将官方 Open WebUI `v0.13.3-slim` 接入独立的受控网关，用于验证客户应用接入、
凭证授权、输入检查、严格缓冲流式输出及审计。它不是实际客户环境；后端合成 provider
仅返回可核对的测试结果，不是真实 LLM，也不产生付费模型调用。

界面入口仅监听 `http://127.0.0.1:8908`，启用账号认证并关闭公开注册。日常验证使用
独立普通用户，管理员账号只用于配置和账号管理。应用数据保存在专用 Docker volume
`scarlet-customer-openwebui-data`；运行进程为 UID/GID `10001:10001`，根文件系统只读。

独立控制面通过 `http://localhost:18912` 浏览，`SCARLET_ORIGIN` 必须与该地址完全一致。
它使用专用数据库和账号，不读取已有平台的数据；与 `127.0.0.1` 上的平台使用不同的浏览器
Cookie 主机。HTTP localhost 仅在显式开发模式下可用，宿主机端口仍只绑定回环地址，
不会关闭来源校验或放宽生产 HTTPS 要求。

## 接入范围

- Open WebUI 只配置 `https://ai-gateway:8905/v1` 和一个受限应用凭证，固定模型为
  `synthetic-media-model`。`model_ids` 是客户端静态目录，不能作为模型就绪或检测生效证明。
- 所有用户在网关侧共享该实例的 `demo-app` / `demo-user` 服务身份。Open WebUI 登录
  身份不会自动成为受信网关身份；本示例不宣称已完成终端用户身份联邦。
- Open WebUI 只加入 `scarlet-customer-demo` 网络，和网关通信；控制面、数据库、provider、
  scanner 使用网关侧的独立网络，不向 Open WebUI 暴露其凭证或内部地址。
- 该共享网络必须为 Docker `internal` 网络，并且只连接 Open WebUI 与网关。Open WebUI
  通过权限 `0600` 的专用 Unix socket 接受请求；非 root Nginx 入口只挂载 socket 卷，
  不挂载应用数据、SQLite 或凭证，也不加入网关网络。入口关闭代理缓冲，支持 SSE 和 WebSocket。
- 聊天请求保留网关已有的严格参数约束。客户端元数据由 Open WebUI 的正常聊天链处理；
  任意额外参数仍由网关拒绝，不为兼容界面而放宽协议。
- 流式请求采用 `strict_hold`：完成检查后才回放，不承诺首 token 的实时低延迟。
  当前界面仅为该模型开放 vision 图片入口，关闭普通文档/网页导入、工具、functions、代码执行
  和自动标题/标签/追问。图片仍须经过网关真实 OCR 与数据策略检查。
  媒体检测能力以网关目录及真实 scanner 回执为准，不能从界面上的模型名称推断。

## 准备私有配置

网关栈使用 `compose.customer-gateway.yaml`，与客户界面分别管理。启动前由操作员准备：

- 当前源码构建的控制面镜像，以及原生迁移、工作区与账号。目录须完成真实回归、独立复核和
  发布，再签发受限应用凭证；不能用未发布配置或手写批准记录替代这些步骤。
- 私有 `demo.env` 中的文件拥有者 UID/GID，以及 `secrets/` 下的数据库、控制面、网关和
  scanner 环境；网关配置目录中的 `catalog.json`、`bindings.json`、`bifrost.json` 与
  `controller.token`。具体挂载位置见网关 Compose，控制面凭证不交给客户界面。
- 各服务的 TLS 证书及可信 CA，`models/ggml-model.bin` 只读模型和两份合成媒体 fixture。
  默认 ASR 模型 SHA-256 为 `921e4cf8686fdd993dcd081a5da5b6c365bfde1162e72b08d75ac75289920b1f`；
  模型不随仓库提供，也不由本示例自动下载。scanner 能力及限制见[媒体检测部署](ai-gateway-media.md)。

例如使用独立控制面镜像标签，避免改用其他正在运行项目的标签：

```sh
docker build -t scarlet-customer-controlplane:local .
CUSTOMER_CONTROLPLANE_IMAGE=scarlet-customer-controlplane:local \
  docker compose --env-file var/customer-demo/demo.env \
  -f compose.customer-gateway.yaml up -d --build
```

上述命令依赖已准备好的私有配置与已发布目录，不表示空目录能够自动完成身份、授权或发布。
启动独立网关栈后，取得它签发的应用凭证和 CA。相关文件只放在已忽略的
`var/customer-demo/`，目录权限 `0700`、私有文件权限 `0600`。Open WebUI 只读取应用凭证，
不要将控制面 token、数据库密码或 scanner token 放入其环境。

使用下面的命令生成实例私有环境和首次账号。将 `CUSTOMER_GATEWAY_CA_FILE` 设置为实际
网关 CA 文件的绝对路径；该 CA 必须验证 `ai-gateway` 主机名。命令不输出密码或 API key。

```sh
export CUSTOMER_GATEWAY_CA_FILE=/absolute/path/to/customer-gateway-ca.crt
python3 - <<'PY'
import json, os, secrets
from pathlib import Path

os.umask(0o077)
root = Path('var/customer-demo/secrets').resolve()
root.mkdir(parents=True, mode=0o700, exist_ok=True)
root.chmod(0o700)
auth_file = root / 'open-webui-auth.json'
if not auth_file.exists():
    auth = {
        'admin': {'email': 'customer-admin@example.invalid', 'name': 'Customer administrator', 'password': secrets.token_urlsafe(32)},
        'user': {'email': 'customer-user@example.invalid', 'name': 'Synthetic customer user', 'password': secrets.token_urlsafe(32)},
        'webuiSecretKey': secrets.token_hex(48),
    }
    auth_file.write_text(json.dumps(auth, indent=2) + '\n')
    auth_file.chmod(0o600)
auth = json.loads(auth_file.read_text())
key = (root / 'app-credential.txt').read_text().strip()
assert len(key) >= 32 and not any(c in key for c in '\r\n;')
ca = Path(os.environ['CUSTOMER_GATEWAY_CA_FILE']).resolve(strict=True)
env = {
    'WEBUI_SECRET_KEY': auth['webuiSecretKey'],
    'WEBUI_ADMIN_EMAIL': auth['admin']['email'],
    'WEBUI_ADMIN_PASSWORD': auth['admin']['password'],
    'WEBUI_ADMIN_NAME': auth['admin']['name'],
    'OPENAI_API_KEYS': key,
}
env_file = root / 'open-webui.env'
env_file.write_text('\n'.join(k + '=' + v for k, v in env.items()) + '\n')
env_file.chmod(0o600)
compose_env = root / 'open-webui-compose.env'
compose_env.write_text('CUSTOMER_GATEWAY_CA_FILE=' + str(ca) + '\nCUSTOMER_OPENWEBUI_ENV_FILE=' + str(env_file) + '\n')
compose_env.chmod(0o600)
PY
docker compose --env-file var/customer-demo/secrets/open-webui-compose.env \
  -f compose.customer-demo.yaml config --quiet
test "$(docker network inspect scarlet-customer-demo --format '{{.Internal}}')" = true
docker compose --env-file var/customer-demo/secrets/open-webui-compose.env \
  -f compose.customer-demo.yaml up -d
```

首次启动通过 `WEBUI_ADMIN_*` 创建管理员，仅在空用户库生效，不重置已有账号。
CA 初始化服务只处理本实例的数据卷和只读 CA，完成后退出；正常应用不以 root 运行。
检查状态时使用 `docker compose ps`；完整 `config` 或容器环境可能含私有值，不应公开输出。

创建普通用户可使用官方管理员 API；以下命令只在内存中使用登录 JWT，不输出它们。

```sh
python3 - <<'PY'
import copy, json, urllib.error, urllib.request
from pathlib import Path

auth = json.loads(Path('var/customer-demo/secrets/open-webui-auth.json').read_text())
def post(path, body, token=None):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request('http://127.0.0.1:8908' + path,
        data=json.dumps(body).encode(), headers=headers, method='POST')
    with urllib.request.urlopen(req, timeout=30) as res:
        return json.load(res)
def login(account):
    return post('/api/v1/auths/signin', {key: account[key] for key in ('email', 'password')})
admin = login(auth['admin'])
assert admin['role'] == 'admin'
try:
    user = login(auth['user'])
except urllib.error.HTTPError as error:
    if error.code not in (400, 401):
        raise
    post('/api/v1/auths/add', {**auth['user'], 'role': 'user'}, admin['token'])
    user = login(auth['user'])
assert user['role'] == 'user'
grants = [{'principal_type': 'user', 'principal_id': user['id'], 'permission': 'read'}]
model = post('/api/v1/models/model/access/update', {
    'id': 'synthetic-media-model', 'name': 'Synthetic gateway model',
    'access_grants': grants,
}, admin['token'])
capabilities = {'builtin_tools': False, 'usage': False, 'vision': True, 'file_upload': True, 'file_context': False,
    'web_search': False, 'image_generation': False, 'code_interpreter': False}
current_capabilities = (model.get('meta') or {}).get('capabilities') or {}
if any(current_capabilities.get(key) != value for key, value in capabilities.items()):
    updated = {key: copy.deepcopy(model[key]) for key in
        ('id', 'base_model_id', 'name', 'meta', 'params', 'is_active') if key in model}
    updated['meta'] = updated.get('meta') or {}
    updated['meta']['capabilities'] = {**(updated['meta'].get('capabilities') or {}), **capabilities}
    updated['access_grants'] = grants
    post('/api/v1/models/model/update?id=synthetic-media-model', updated, admin['token'])
post('/api/v1/retrieval/config/update', {
    'ALLOWED_FILE_EXTENSIONS': ['png', 'jpg', 'jpeg'], 'FILE_MAX_SIZE': 4,
}, admin['token'])
PY
```

模型访问 API 在记录不存在时创建最小基础模型记录，并覆盖该模型的授权列表。本专用实例仅
授予上述普通用户 `read`，不使用通配用户或访问控制旁路。新建基础模型采用 `is_active=true`、
`base_model_id=null` 和空生成参数；更新能力时保留其他元数据与参数。vision 元数据只影响
界面入口，不提供网关执行权限。
`builtin_tools=false` 必须在该模型能力中显式关闭：官方浏览器会话带有 `session_id` 时，
内建时间/交互工具的注入不依赖插件总开关。网关不接受此类工具字段，不通过剥离未知字段或
放宽模型协议来兼容它们。
`file_upload=true` 是原生上传菜单的总开关，须与 `vision=true`、普通用户的图片上传权限同时
启用；`file_context=false` 关闭文件文本检索注入。普通会话选图会先规范化/压缩图片，再以
`process=false` 上传到客户应用自己的文件服务；临时会话直接保留 data URI。发送到网关时
仍须满足目录授权、媒体字节格式、预算和 OCR 检查。

打开本地页面，使用私有账号文件中的普通用户登录。测试仅使用合成内容，分别核对正常请求、
被拒输入、完整流式回放和网关实际回执；客户端静态模型列表和一次 HTTP 200 不能代替这些检查。

## 配置与限制

私有 CA 通过 `AIOHTTP_CLIENT_SESSION_SSL` 配置实际聊天连接；同时为其他 HTTP 客户端设置
`SSL_CERT_FILE` 和 `REQUESTS_CA_BUNDLE`。不关闭证书或主机名验证。更换 CA 后须重跑初始化
服务并重建应用，让 TLS 上下文重新载入证书。先停止本 Compose 的应用与入口，再执行
`up -d --force-recreate`，保留现有数据卷和账号；不要删除数据库或应用凭证来刷新 CA。

`OFFLINE_MODE` 及遥测设置关闭自主更新、模型下载和相关遥测；Ollama、direct connections、
direct integrations 均关闭。应用后端通过内部网络隔离，入口 CSP 的 `connect-src` 限制
正常浏览器页面的连接目标。官方版本的部分文件/网页导入权限仍属于界面限制；修改过的客户端
也能请求由其自身浏览器执行的 direct RPC，不能据此声称控制了用户任意自带浏览器或密钥的
网络访问。网关仍独立执行凭证、目录、参数、输入/输出和预算约束。
`connect-src` 允许 `data:`，仅配合官方选图流程将本地 data URI 转为 Blob，不增加额外网络主机。
`ENABLE_PERSISTENT_CONFIG=false` 防止旧数据库配置覆盖环境，不是管理员配置锁。
上传配置列出 `png,jpg,jpeg` 和 4 MiB 大小上限，但该版本仅在 `process=true` 时强制检查
文件扩展名，原生图片的 `process=false` 路径不受这一扩展名检查，chooser 也不是格式安全门。
不能将其表述为文件存储端已强制仅收 PNG/JPEG。演示只选 PNG/JPEG，真实外发格式及内容由
网关独立解码、目录白名单和 scanner 强制检查；未允许格式不会据此绕过网关。

保留或删除数据卷由实例负责人决定。停止界面可运行同样的 Compose 命令加 `down`；默认保留
专用数据卷，不应清理其他工作区、生产平台或其他项目的容器和卷。

官方依据：[固定版本源码](https://github.com/open-webui/open-webui/tree/b0e6098e940942281d9c83f3529ea117287a3a22)、
[OpenAI 连接与静态目录](https://github.com/open-webui/open-webui/blob/b0e6098e940942281d9c83f3529ea117287a3a22/backend/open_webui/routers/openai.py)、
[认证接口](https://github.com/open-webui/open-webui/blob/b0e6098e940942281d9c83f3529ea117287a3a22/backend/open_webui/routers/auths.py)、
[原生上传菜单](https://github.com/open-webui/open-webui/blob/b0e6098e940942281d9c83f3529ea117287a3a22/src/lib/components/chat/MessageInput/InputMenu.svelte)、
[文件上传校验](https://github.com/open-webui/open-webui/blob/b0e6098e940942281d9c83f3529ea117287a3a22/backend/open_webui/routers/files.py)、
[离线模式](https://docs.openwebui.com/tutorials/maintenance/offline-mode/)。
