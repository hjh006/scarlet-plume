# OpenMetadata 部署与自动采集

样例部署使用 OpenMetadata 2.0.7、PostgreSQL16 和 Elasticsearch9.3.0；服务端和官方 ingestion 镜像均固定多架构 digest。OpenMetadata 界面为 `http://127.0.0.1:8585`，赤翎仍为 `http://127.0.0.1:8890`。只发布回环8585，搜索、数据库与管理端口不向宿主机发布；不部署 Airflow。

## 启动与凭据

先启动现有赤翎和 PostgreSQL 样例。下列命令在仓库根目录执行：

```sh
node docker/openmetadata/bootstrap.mjs --init
docker compose --env-file .env.scarlet-local --env-file .env.scarlet-openmetadata -f compose.openmetadata.yaml up -d --wait --wait-timeout 600 openmetadata
node docker/openmetadata/bootstrap.mjs
docker compose --env-file .env.scarlet-local --env-file .env.scarlet-openmetadata -f compose.openmetadata.yaml --profile collectors up -d metadata-ingestion metadata-discovery
```

初次完整迁移和系统目录初始化需要数分钟。`bootstrap.mjs`生成每实例随机数据库口令、管理员口令、Fernet密钥和RSA签名密钥，保存在忽略的 `.env.scarlet-openmetadata` / `.scarlet-openmetadata`，文件权限600。私钥通过一次性容器复制到专属卷，以OpenMetadata用户1000、权限600读取。不得删除后重新生成旧实例正在使用的Fernet密钥，否则原连接密文将无法解密。

脚本仅对本地8585执行初始管理员改密，并使用已有 `.env.scarlet-login` 登录赤翎8890，创建一个工作区 `local` 的发现任务。不会将上游JWT、样例数据库口令或管理员凭据写入平台任务或公开日志。OpenMetadata自身通过官方数据库 secrets manager 和每实例Fernet密钥保存连接秘密；它与赤翎数据库分开。

## 两段自动采集

1. `metadata-ingestion`每60秒运行官方 `MetadataWorkflow`，使用样例只读账号连接 `sample-db:5432/scarlet_sample`，在 `sourceConfig` 中限定 `inventory`。采集表、视图、字段说明、类型和约束；不运行 profiler、不把行样本或查询日志上传。此专用合成服务启用 `overrideMetadata` 跟随源说明变更；赤翎自身的人工分类仍独立优先。失败仅记录固定状态元数据。
2. `metadata-discovery`使用独立令牌领取赤翎任务，从真实OpenMetadata REST API读取表、字段、标签、约束和已有lineage，写入目录、G02检查证据及合规报告。任务频率、连接诊断和立即采集可在赤翎“多源自动发现”页面操作。上游60秒周期独立于此页面频率。

平台 `-local-docker` 仅新增允许固定 `http://openmetadata:8585[/api]`、PostgreSQL类型、`scarlet_sample` 和单一 `inventory` schema。其他私网主机、端口、路径、查询参数或数据库继续拒绝；生产连接继续要求验证HTTPS。

目录来源清楚标记 `content=not_collected`、`permissions=unknown`。OpenMetadata的所有者、标签和字段说明不能证明数据库ACL或真实内容，也不能将这些G02检查自动提交为内容分类通过。上游未观测的lineage保持未知。最新失败、配置改变或范围失效时，旧成功记录不作为当前有效采集；历史证据保持不可变。

这里的 `current` 表示赤翎最近一次HTTP目录拉取有效，不证明上游数据库抽取仍最新。上游工作流的成功时间由独立ingestion日志记录；验证更新时应依次核对源PostgreSQL说明、官方ingestion成功、OpenMetadata说明和赤翎新目录版本，不能用重复读取旧目录替代源采集证明。

## 读写身份与更新

官方ingestion使用独立 `IngestionBotRole` 令牌。赤翎采集器使用自建 `ScarletMetadataReadRole`，明确拒绝服务连接、用户/角色/策略管理、表修改、样本、查询及数据画像读取。OpenMetadata默认给新bot附加 `DefaultBotRole`，仅声明DataConsumer不足以限制连接秘密读取；脚本会显式移除默认角色，再设置专用策略。部署后应验证表接口允许读取、服务连接读取与服务创建均被拒绝。

两个上游JWT有效期30天，过期时间私存在同一env。更新命令会重新核对角色、生成新JWT，随后重建两个采集侧车以加载新环境：

```sh
node docker/openmetadata/bootstrap.mjs --rotate
docker compose --env-file .env.scarlet-local --env-file .env.scarlet-openmetadata -f compose.openmetadata.yaml --profile collectors up -d metadata-ingestion metadata-discovery
```

## 部署验证

- 从样例源创建或修改合成表、视图和说明，经官方工作流采集后核对 OpenMetadata 与平台目录；不通过平台接口伪造源采集结果。
- 使用无效令牌验证 `collection_failed` 和 `current=false`，恢复授权后重新采集。元数据不能作为内容确认或完整 ACL 证据，旧目录和报告快照不应被改写。
- 核对来源当前性、采样缺口、字段和约束，以及读取服务连接被拒绝。`TestDiscoveryOpenMetadataLiveMetadataCoverage` 需要独立实例；缺少环境时跳过不代表验证通过。

Compose中的连接池和内存限制用于本地合成样例，不是生产容量基准。部署方需验证实际负载、资源上限和恢复行为。

## 上游依据与许可

版本与部署依据：[2.0.7官方发布](https://github.com/open-metadata/OpenMetadata/releases/tag/2.0.7-release)、[官方Docker部署](https://docs.open-metadata.org/v2.0.x/deployment/docker)、[外部工作流运行](https://docs.open-metadata.org/v2.0.x/deployment/ingestion)、[PostgreSQL连接器示例](https://github.com/open-metadata/OpenMetadata/blob/2.0.7-release/ingestion/src/metadata/examples/workflows/postgres.yaml)。

官方镜像保持独立第三方许可，未将上游源码或二进制复制成赤翎自研代码。特别是2.0.7 ingestion采用[Collate Community License1.0](https://github.com/open-metadata/OpenMetadata/blob/2.0.7-release/ingestion/LICENSE)，含竞争性托管服务使用限制。部署或提供托管组合服务时，应按所用组件的实际版本和许可确定可提供范围。
