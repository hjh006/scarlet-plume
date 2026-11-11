# 数据流、API生命周期与受控溯源

本功能参考用户提供的红途产品材料中的能力方向，使用赤翎自己的数据模型和界面实现。材料分析及剩余差距见 [能力映射](hongtu-capability-mapping.md)。

## 来源与关系

| 来源 | 自动获得的事实 | 关系的含义 |
| --- | --- | --- |
| PostgreSQL任务 | 已采集库表、字段、权限、分类及批次 | 目录资产与分类；不能仅凭相似名称推断应用实际访问 |
| OpenAPI契约 | 服务别名、方法、路由模板、鉴权声明、敏感字段候选 | 契约声明及版本差异，不等同于运行时事实 |
| 独立Agent/网关日志 | HTTP方法、路由模板、响应状态，可选源端JSON字段类型统计 | 已观测请求；无法从网关日志直接确定数据库查询目标 |
| 应用OTLP | 受控trace/span引用、可选主体引用、数据库系统类型、明确目标与字段统计 | 只有目标的来源别名、库、schema、表与同工作区实际目录匹配时，才建立精确库表关联 |

运行时来源的服务别名必须与契约的来源别名一致。数据库目标中的来源别名指向PostgreSQL采集任务，与服务别名分别配置。只知道数据库系统类型时展示类型级节点。字段统计和数据库目标同时出现，只能证明它们属于该请求的受控观测；多目标时不能证明每个字段逐一来自哪个表。

图、过滤、历史快照及溯源均只覆盖已接入来源。不同采集渠道的计数分别展示，避免把同一个请求重复相加。保留窗口结束、采集暂停或接入缺口会影响可见关系。“未再观察”不代表业务对象已经删除。

## API生命周期

“API生命周期”结合最新契约和实际观测，区分已登记并观测、仅登记、仅观测和上一版移除候选。可按服务与运行时来源筛选、导出，并对比契约中的接口增减、鉴权、弃用声明及敏感字段候选变化。仅首次导入时没有历史差异。移除候选须结合覆盖范围进一步确认。

## 源端JSON识别

独立Go Agent通过可选的 `-inspect-json` 开启源端识别，默认关闭。它只在客户侧读取受限JSON，最多64 KiB、32条路径、8层结构，上报字段路径、类别、方法、命中数量与观察数量。未知动态键会遮蔽并产生覆盖警告，不上传原始业务值。

识别结合字段元数据和有限格式校验，包括邮箱、电话、身份证校验和银行卡Luhn校验。格式命中是候选信号，不能自动证明个人身份、重要数据或法定行业等级。请求或响应超限、动态键、无效JSON等缺口进入采集警告。HTTPS需在受信任的终止点读取明文，或由应用在加密前生成受控遥测；平台不能通用解密任意密文。

## 本地真实查询样例

`compose.flow-sample.yaml` 提供显式应用追踪样例。它实际查询已有样例库的 `inventory.classification_contacts`，返回合成邮箱并产生HTTP/数据库父子span。样例按响应与数据库字段分别生成类型统计，以HMAC生成去标识主体，不发送SQL、原值或登录凭据。

1. 先按README启动本地平台和PostgreSQL样例库。在“运行时来源”创建OTLP来源，服务别名填写 `Scarlet Flow Sample`。
2. 在忽略提交的 `.env.scarlet-flow` 中设置随机 `SCARLET_FLOW_PASSWORD`、不少于32字符的 `SCARLET_FLOW_SUBJECT_SALT`、新来源的 `SCARLET_FLOW_OTLP_TOKEN`，以及现有PostgreSQL任务的精确来源别名 `SCARLET_FLOW_DATA_SOURCE`。
3. 合并已有部署配置，先构建、显式迁移数据库，再启动服务：

```sh
scarlet_flow() {
  docker compose --env-file .env.scarlet-local --env-file .env.scarlet-runtime \
    --env-file .env.scarlet-flow \
    -f compose.local.yaml -f compose.sample.yaml -f compose.runtime.yaml \
    -f compose.flow-sample.yaml "$@"
}
scarlet_flow build app flow-sample
scarlet_flow run --rm --no-deps -T app -local-docker -mode migrate
scarlet_flow up -d --no-build --wait
```

4. 以同一服务别名导入 [样例OpenAPI](../examples/flow-sample-openapi.json)。访问 `http://127.0.0.1:8893/api/contacts`，BasicAuth用户名为 `scarlet-demo`，密码为本地配置值。
5. 在“数据流转”查看应用→接口→已采集库表及去标识主体路径，核对来源和观察窗口，再按trace或主体反查。采集配置、访问授权与来源凭据互相独立。

样例响应通过 `X-Scarlet-Telemetry: delivered` 表示遥测已被平台接受，失败时返回 `failed` 并记录不含凭据的诊断。业务响应成功不替代采集回执。升级时需显式执行迁移，服务的健康端点不检查每项业务表是否已经迁移。

当前样例用于验证链路与证据来源。浏览器经营字段打标、全部数据库/语言的内部探针、行为异常检测、全协议解析和外部系统自动处置仍需分别实施与验收。
