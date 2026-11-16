# Wazuh 部署与事件接入

本地采用官方 Wazuh manager **4.14.10** 多架构镜像，固定索引摘要 `sha256:dcda4869e2f792e92f03bce93f85b4bdf103756ea61cfca8ba9f3eabeb05365b`。该摘要包含ARM64与AMD64清单。Manager运行真实logcollector、JSON decoder、analysisd规则及custom integrator，原始告警保存在`wazuh_logs`卷。平台负责事件、整改复核和报告展示，本样例不部署Wazuh indexer/dashboard，也未开启漏洞情报扫描或主机审计。

## 来源与取证

| 环节 | 实际行为 |
| --- | --- |
| 合成接口 | 8897的`GET /api/export`由`/data/mode`控制；`public`返回固定合成JSON，其他值、缺失或读取失败返回403。没有真实客户数据。 |
| 周期测量 | Checker每15秒实际请求健康检查及匿名导出接口；只在结果变化时写入JSON日志。可用`check-once`强制重新请求并留证。 |
| 原生分析 | Manager从`/scarlet-input/checks.json`采集。安装的自定义规则100501识别已确认匿名200，100502识别健康204且导出403，100503表示无法判定。规则由实际decoder/analysisd执行。 |
| 来源投影 | 原生custom integrator在平台发送前先将白名单元数据写入持久spool。固定校验manager agent000、日志位置、规则、级别、scope、状态、proof和测量时间；平台observedAt采用实际HTTP测量时刻，analysis时间只校验最大120秒排队延迟（原生毫秒精度允许1ms舍入），不能把旧测量因晚分析变为新复测；原始日志、响应内容、主机名、描述及任意data不上传。 |
| 事件身份 | 平台`eventId`原样保留Wazuh `alert.id`，可反查`/var/ossec/logs/alerts/alerts.json`。100501与100502都映射到`wazuh:scarlet.public_export.v1`、服务`Scarlet Wazuh Export`、`GET /api/export`。映射仅适用于该已安装合成规则对，不根据任意告警文案推断修复。 |
| 耐久交付 | 独立worker以与固定镜像Integrator相同的UID999运行，读取mode0600投影文件，按源测量时间顺序处理spool，调用既有Go `scarlet-event-bridge`。失败保留元数据，成功才删除；相同原生告警重放由平台幂等接收。首次健康且此前无已投递风险时不创建虚假的已修复检查。 |
| 闭环 | 来源`cleared`只登记实际新测量；检查仍需整改记录、满足时间顺序的新鲜来源复测、提交和独立复核。没有新告警、停机、断链或无法判定均不等于修复。 |

## 启动

先按主README启动平台并构建`scarlet-plume-local:dev`。由负责人在“运营与处置 → 集成来源”新建独立的安全事件来源，将一次性token保存在权限0600、git忽略的`.env.scarlet-wazuh`中。该文件另设随机强口令`SCARLET_WAZUH_API_PASSWORD`和用户名`SCARLET_WAZUH_API_USER`；manager不持有平台token，不发布1514/1515/55000到宿主机。

```sh
docker compose --env-file .env.scarlet-local --env-file .env.scarlet-wazuh \
  -f compose.local.yaml -f compose.wazuh.yaml \
  build wazuh-manager wazuh-export-sample wazuh-bridge
docker compose --env-file .env.scarlet-local --env-file .env.scarlet-wazuh \
  -f compose.local.yaml -f compose.wazuh.yaml \
  up -d --no-deps wazuh-manager wazuh-export-sample wazuh-checker wazuh-bridge
```

现有其他覆盖文件不由此命令删除；不要使用`--remove-orphans`或清空数据卷。样例内存上限分别为manager768MiB、接口/checker/bridge各96MiB。原始告警和队列留在来源卷，生产需为告警留存、容量和访问权限另行配置。

## 合成修复与强制新复测

以下命令仅操作本样例卷，不会改变其他应用。初次创建默认拒绝导出。

```sh
# 产生一个明确的合成风险：实际匿名导出将返回200。
docker exec scarlet-plume-local-wazuh-export-sample-1 python -c \
  "from pathlib import Path; Path('/data/mode').write_text('public\\n')"
# 实际修复：匿名导出变为403。
docker exec scarlet-plume-local-wazuh-export-sample-1 python -c \
  "from pathlib import Path; Path('/data/mode').write_text('private\\n')"
# 重新发起HTTP测量，不复用缓存结果。
docker exec scarlet-plume-local-wazuh-checker-1 \
  python /opt/scarlet-wazuh/sample.py check-once
# 检查原生服务运行状态。
docker exec scarlet-plume-local-wazuh-manager-1 /var/ossec/bin/wazuh-control status
```

读取原始Wazuh告警须在来源端进行。若需手动补偿，以UID999读取原生告警文件并再次调用安装的`custom-scarlet-export`，保持原alert.id和原始测量时间；不要伪造新的安全结论。源码中`test_project.py`覆盖固定规则映射、未知/错误proof不能clear、原内容剔除及私有原子spool重放。

## 官方依据

- [官方Docker升级说明与4.14.10镜像](https://documentation.wazuh.com/current/deployment-options/docker/upgrading-wazuh-docker.html)
- [JSON decoder](https://documentation.wazuh.com/current/user-manual/ruleset/decoders/json-decoder.html)
- [自定义规则语法](https://documentation.wazuh.com/current/user-manual/ruleset/ruleset-xml-syntax/rules.html)
- [原生Integrator](https://documentation.wazuh.com/current/user-manual/manager/integration-with-external-apis.html)

本样例仅覆盖固定合成配置检查与事件工作流，不能据此推导所有Wazuh规则、其他安全设备或完整主机覆盖均已验证。

## 部署验证

产生合成匿名导出风险，核对原生规则100501、平台事件及检查；停止合成接口后，规则100503应保持未知，不能清除风险。恢复受控访问并重新测量后，规则100502仅提供新复测证据，检查仍需实际整改记录与独立复核。

核对平台采用原始测量时间，排队与重投不应刷新证据时刻。停止并重建worker后验证持久spool继续投递、原事件幂等接收；格式错误或投递失败的条目应保留并明确报告。来源投影测试入口为 `docker/wazuh/test_project.py`，不能代替其他规则和设备的接入验证。
