# 分类知识、本地训练与证据交付

2026-11-12。入口为分类分级工作台及场景内的“识别与证据能力”。本地规则引擎继续默认运行。知识、模型及交付分别保存版本和采用依据，不自动覆写人工分类或法定类别。

## 知识检索与模型辅助

负责人可维护最多100份文档，每份正文上限32KiB，包含来源链接、启用状态及摘录发送权限。更新递增修订并重算摘要。链接是引用，平台不会主动抓取该地址。

`local-lexical-v1` 使用元数据词项及中文字符对，在本地检索最多5个精确原文片段，返回文档修订和摘要。当前不使用向量数据库，不以检索次数推断识别精度。

本地检索不调用模型。知识增强建议要求已启用模型及元数据授权、文档摘录发送权限，并由操作人预览元数据和摘录、逐次明确授权。调用前后核对资产、模型配置和文档版本；变化时拒绝保存过期结果。支持已登记的Chat Completions与Responses兼容协议。

输入中的指令作为不可信资料处理，不开放工具调用。建议保存当次知识快照，文档后续变化不改写旧建议。合成协议夹具不表示真实供应商识别精度已验收。

| 接口（工作区classification下） | 用途 |
| --- | --- |
| `GET/POST /knowledge`、`PUT /knowledge/{id}` | 查询、新建及修订 |
| `POST /knowledge/search` | 对实际资产及修订本地检索 |
| `POST /knowledge/suggest` | 明确授权后生成带知识快照的参考 |

## 可训练的本地分类器

`multinomial-naive-bayes-v1` 是可复现的本地监督模型，使用英文词项和中文字符对，给出经营、个人信息或敏感个人信息参考。它不自动认定重要/核心数据或法规通过。

输入是经过授权的字段名、业务描述等元数据及标签，显式划分train/evaluation。单次最多500条、合计512KiB，每条最多1000字符；至少6条训练样本、2个类别且每类至少2条，并有至少2条独立评估样本。相同规范化文本不得重复用于训练或评估。

每次固定数据集、模型摘要、算法、阈值和评估结果。准确率、精确率、召回率、F1及混淆计数仅描述指定评估集。生产覆盖、误报漏报和吞吐需另测。`relativeScore` 是类别间相对分数，不是校准的正确概率；无已知特征或未达阈值返回未知。

预测读取实际资产及修订，保存模型版本和参考结果，不写分类表。采用结论继续走分类确认、标准依据及独立发布流程。

| 接口（工作区classification下） | 用途 |
| --- | --- |
| `GET/POST /training/models` | 模型清单或训练评估 |
| `GET /training/models/{id}` | 查看冻结摘要与评估 |
| `POST /training/models/{id}/predict` | 实际资产的本地参考 |

## 发布与安全事件交付

负责人配置接收方并取得一次性执行令牌，平台只保存令牌摘要。地址要求HTTPS，显式本机验收可用回环HTTP；禁止URL中的账号、查询参数和片段。接收方业务令牌只放客户侧agent环境。

用户选择已独立发布的分类快照创建任务，冻结目标名称/地址/修订、资源和载荷摘要。同目标修订与资源重复提交返回同一任务；失败重试保持同一交付ID。目标变更或令牌轮换取消未完成旧任务，已完成任务保留原目标。

安全事件自动订阅默认关闭，负责人明确启用并选最低等级后，新事件的受限投影进入同一队列；来源重放不重复通知，也可人工投递既有事件。企业Webhook可接SOC/工单，再由其分发消息；当前没有原生邮件发送服务。

```sh
export SCARLET_URL=https://scarlet.example.org
export SCARLET_DELIVERY_ENDPOINT=https://receiver.example.org/scarlet
# 在受保护环境配置 SCARLET_CLASSIFICATION_DELIVERY_TOKEN
# 可选 SCARLET_DELIVERY_RECEIVER_TOKEN / SCARLET_DELIVERY_CA_FILE
scarlet-classification-delivery-agent -once
```

执行器只访问本地配置且与任务完全一致的地址，不跟随重定向，继续验证TLS。平台令牌只给平台，接收方令牌只给接收方。接收方按`Idempotency-Key: scarlet-delivery:{id}`持久去重并返回：

```json
{"deliveryId":1,"payloadDigest":"<完整规范化载荷SHA256>","status":"accepted","remoteRef":"<接收方记录标识>"}
```

成功回执表示确认接收，不等于内部标签、策略、工单或外部程序完成。网络不确定或回执丢失后可能重投同一ID，接收方必须持久幂等，不宣称全链路恰好一次。

| 接口 | 用途 |
| --- | --- |
| `/classification/delivery-targets`、`/{id}/rotate` | 接收方及令牌 |
| `POST /classification/publications/{id}/deliveries` | 交付已发布快照 |
| `POST /operations/events/{id}/deliveries` | 交付安全事件 |
| `GET /classification/deliveries` | 过滤回执 |
| `POST /api/v1/classification-delivery-agent/claim`、`/receipts` | 客户执行器领取及回执 |

本轮只用隔离数据库、合成模型协议服务和本地接收方验收，没有向供应商系统、真实模型或外部消息服务发送数据。
