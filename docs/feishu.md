# 飞书文档权限取证和复测

赤翎的客户侧采集器读取一篇新版飞书文档的协作者列表，检查一个指定的、与飞书应用对应的 `openid` 直接授权条目。它调用中国版飞书开放平台的 `GET /open-apis/drive/v1/permissions/{token}/members?type=docx`。飞书当前文档为此接口定义单次列表响应。接口报错、响应缺少列表或明确提示结果不完整时，采集失败且不生成观察记录。 [获取云文档协作者](https://open.feishu.cn/document/server-docs/docs/permission/permission-member/list)

`present: true` 表示该直接条目存在，`permission` 区分 `view`、`edit` 和 `full_access`。`present: false` 表示完整响应中没有这个 `openid` 条目。通过群组、部门、链接、知识库或其他成员标识取得的权限需要另行验证。需要验收实际能否打开文档时，请由该测试协作者本人在整改前后分别打开测试文档，并记录观察结果。 [云文档授权方式](https://open.feishu.cn/document/server-docs/docs/docs-overview)

## 验证状态

连接器使用模拟 HTTPS 权限接口验证了直接授权存在和撤销后条目消失。工作台使用合成快照和真实 PostgreSQL 验证了整改、复测与独立复核。当前缺少飞书测试租户，以下步骤用于真实权限验收。

## 准备联调环境

1. 在获准的飞书测试租户创建一篇独立的空白新版文档。将链接访问范围设为仅已有权限的协作者，避免使用知识库节点和带继承权限的文件夹。准备一个经同意的临时测试协作者。外部协作者演练应使用租户策略已允许的测试身份。
2. 使用测试应用的 `docs:permission.member:retrieve` 权限。通过应用或用户身份获得能够查看测试文档协作者列表的短期访问令牌。如果使用应用身份访问用户创建的文档，由文档所有者先授权该应用。
3. 从新版文档链接的 `/docx/` 段取出资源 token。取得同一应用下的目标协作者 `open_id`，以该身份建立待撤销的直接授权。选择能看到协作者列表的采集身份，确保其读取权限在演练中保持有效。 [获取 Open ID](https://open.feishu.cn/document/faq/trouble-shooting/how-to-obtain-openid)
4. 在客户侧终端提供下列环境变量。三个值均留在本机，联调记录使用后续生成的脱敏观察记录。请将采集机与赤翎服务端的时钟同步，以便校验整改前后的取证顺序。

| 环境变量 | 用途 |
| --- | --- |
| `SCARLET_FEISHU_ACCESS_TOKEN` | 短期 `tenant_access_token` 或 `user_access_token`，不带 `Bearer ` 前缀 |
| `SCARLET_FEISHU_DOC_TOKEN` | 新版测试文档的资源 token |
| `SCARLET_FEISHU_MEMBER_OPEN_ID` | 同一应用下的目标协作者 `open_id` |

## 完成一次整改和复测

1. 在 `backend/` 下计算检查目标摘要，再采集整改前的权限。确认返回 `present: true` 和实际权限。如果未找到条目，先核对应用、协作者标识类型和文档资源 token。

   ```sh
   umask 077
   go run ./cmd/feishu -target-only
   go run ./cmd/feishu > feishu-before.json
   ```

2. 在赤翎工作区先登记测试文档对应的数据资产。进入「监管检查」新建检查，选择「G03 · 访问控制、加密与备份」，将检查范围写为该测试文档的指定协作者直接授权，绑定同一飞书目标摘要。在「证据记录」展开登记表单，选择「飞书权限清单」并导入整改前的 JSON，然后提交检查。用另一名具备复核权限的工作区成员核对证据，确认目标授权仍存在并要求整改。
3. 文档负责人在飞书文档的「分享」中移除该目标的直接协作者权限。保留采集身份的权限。整改执行人回到赤翎，通过「手工登记」添加类型为「整改记录」的证据，记下移除操作和实际发生时间。
4. 在同一客户侧终端对同一目标重新采集。确认新记录的 `targetDigest` 一致、`observedAt` 晚于整改，且 `present: false`。若接口返回失败，检查令牌和文档权限后重新采集。

   ```sh
   go run ./cmd/feishu > feishu-after.json
   ```

5. 在同一检查的证据表单选择「飞书权限清单」，导入新采集清单作为复测证据，然后重新提交。独立复核人核对两次记录和整改说明，确认直接授权条目已消失。验收记录区分直接授权复测和目标协作者的实际访问尝试。

两次 JSON 记录包含目标摘要、采集时间和目标权限状态。飞书原始协作者列表、文档内容、凭据和原始协作者标识留在客户侧。操作人员上传的 JSON 不含飞书签名。平台保存的摘要用于匹配和去重，独立复核时由复核人核查来源和采集过程。 [访问凭证说明](https://open.feishu.cn/document/uAjLw4CM/ugTN1YjL4UTN24CO1UjN/trouble-shooting/how-to-choose-which-type-of-token-to-use)
