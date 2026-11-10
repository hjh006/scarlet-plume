# Django 历史记录迁移到 Go

导入器在同一个 PostgreSQL 数据库内读取 `public` 的 Django 表，并将业务记录写入 `plume`。迁移覆盖工作区、成员角色、资产分类、处理活动、监管规则、检查、证据和操作历史。检查状态、责任团队、原始时间和作者对应关系会保留。目标表重新分配 Go ID，同时保存 Django 来源 ID。历史检查原本只关联资产，迁移时沿用该关系。新检查可在工作台明确关联处理活动。

导入的人员记录限于工作区成员和历史操作关联人。Django 密码哈希、会话、管理员权限和邮箱留在原系统。导入后，账号需要独立激活才能登录 Go。

## 准备与预检

1. 备份 Django 数据库。先将备份恢复到隔离环境验证，确保 `public` 中有本仓库 Django 迁移建立的业务和用户表。目标 `plume` 位于同一数据库。
2. 通过本机环境注入 `SCARLET_DB_DSN`，为远程连接配置 `sslmode=verify-full`。在 `backend/` 下执行 Go schema 迁移，然后运行默认的只读预检。使用本机临时数据库时，可对下列命令显式加 `-allow-insecure-loopback`。

   ```sh
   go run ./cmd/server -mode migrate
   go run ./cmd/import-django
   ```

3. 检查返回的 `mode: "check"` 和各类记录数量。若用户名或工作区标识已存在，或同一规则编码的内容有差异，预检报错并停止。重名项目需要先核定归属，或在保留 Django 表的独立目标副本演练。导入器不会将 Django 成员权限合并到同名 Go 账号。相同编码且内容一致的规则沿用 Go 条目，新增规则复制到 Go。
4. 迁移账号需要读取 Django 源表。正式导入在源表上申请 `SHARE` 锁，PostgreSQL 18 可授予该任务账号源表的 `SELECT` 和 `MAINTAIN` 权限。目标账号还需读取和插入目标表、使用目标序列并锁定规则表。可用已有的 schema 迁移角色执行，运行时的 Go 服务账号无需访问 Django 表。 [PostgreSQL 18 锁权限](https://www.postgresql.org/docs/18/sql-lock.html)

## 应用与切换

1. 核对备份和预检结果。在正式切换窗口暂停 Django 写入和 Go 侧变更，使用同一数据库配置执行导入。

   ```sh
   go run ./cmd/import-django -apply
   ```

2. 成功时，报告返回 `mode: "applied"`、完成时间和记录数量。导入在一笔事务内写入记录与完成标记。锁冲突或目标约束失败会回滚此次导入的记录。重复执行返回 `mode: "already-applied"` 和首次统计。这是一次性快照，切换期间应维持 Django 写入暂停。
3. 为需要登录的迁移成员逐一激活。账号原来须处于启用状态且有可用密码，并且在 Go 中仍有工作区成员资格。使用有权更新 `plume.users` 的迁移账号。通过本地环境给 `SCARLET_IMPORT_USER_PASSWORD` 设置新的密码，再运行下列命令。激活只接受等待激活的账号，重跑不会覆盖已设置的密码。

   ```sh
   go run ./cmd/import-django -activate-user '原 Django 用户名'
   unset SCARLET_IMPORT_USER_PASSWORD
   ```
4. 核验资产分类、活动、检查状态、证据时间和复核记录。对一条历史待复核检查，使用独立复核账号验证仍能看到当前待办。Go 检查的详情和导出保留 `djangoId` 供对照。完成验收后切换业务入口，原 Django 数据库记录可用于历史查询和对照。Go 开始写入后，如需回切，应先核对切换以来的新增和变更数据。

原 Django 数据保持原样。Go 开启写入前，可使用该数据继续核对迁移结果。独立复核仍使用当前成员角色与历史作者判断，激活账号不会改变原有角色或证据归属。
