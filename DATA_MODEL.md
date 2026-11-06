# 首版数据模型与事务

状态：依 ADR-0003 修订的逻辑模型与不变量。T012 负责写 goose 迁移和 sqlc 查询，并用真实 PostgreSQL 集成测试验证；目前没有迁移。数据服务的统一记录字段见 [DATA_SERVICE](docs/DATA_SERVICE.md) §4，Harness 的步骤与结论结构见 [HARNESS](docs/HARNESS.md)。

## 1. 表与所有权

### 1.1 用户私有（带非空 workspace_id）

| 表 | 关键字段与唯一性 | 模块 |
| --- | --- | --- |
| users | issuer+subject 唯一；persona；invited_by | identity |
| workspaces | 每用户一个；owner_user_id 唯一 | identity |
| sessions | hash(session_id)、user、workspace、expires_at、csrf_hash、revoked_at | identity |
| research_plans | id、skill_id/version、subject_id、params、as_of、sections、plan_hash、expires_at、consumed_task_id | harness |
| research_tasks | id、plan_id 唯一、skill_id/version、subject_id、status、revision、epoch、deadline、budget、snapshot_hash、report_ref、reason_code | research |
| task_events | task+sequence 唯一；event_type、expected_revision、epoch、payload | research |
| task_steps | task+epoch+step_key+input_hash 唯一；status、lease_until、attempt、output_ref、output_hash | harness |
| model_calls | step_id+attempt 唯一；profile、model_id、observed_model_version、request_hash、状态（SENT/SUCCEEDED/FAILED/UNKNOWN）、usage、cost、reserved_cost | harness |
| usage_ledger | task_id+entry_id 唯一；entry_type（reserve/actual/release/unknown_hold）、amount（NUMERIC）、price_table_version | research |
| claims | task+epoch+section+claim_ref 唯一；kind、text、refs、numbers、verdict、removed_reason | harness |
| report_versions | report_id+version 唯一；task_id 唯一；冻结的渲染 JSON 引用与哈希、snapshot_hash、plan_hash、skill/rule/prompt 版本 | reports |
| publications | task_id+epoch 唯一；report_version、delivered_at | reports |
| watchlist_items | workspace+entity 唯一；tags、note、added_at、added_price、version | watchlist |
| watchlist_alerts | item+kind 唯一；threshold、enabled、last_triggered_trade_date | watchlist |
| watchlist_snapshots | workspace+item+trade_date 唯一；指标值、triggered | watchlist |
| feedback | report+version+user+idempotency_key 唯一；kind、claim_ref、comment | feedback |
| idempotency_keys | workspace+方法+规范化路由+key 唯一；request_hash、response_ref、expires_at | platform |

### 1.2 平台共享（无 workspace_id，用户角色只读）

| 表 | 内容 | 模块 |
| --- | --- | --- |
| invites | 邀请码哈希、额度、过期时间、创建者 | identity |
| entities / entity_aliases | 实体、类型、代码、名称、拼音、曾用名、生效区间 | catalog |
| capabilities | 数据集 × 厂商 × 市场的启用状态、覆盖区间、许可用途、更新时间 | catalog |
| providers / provider_datasets | 厂商与产品、口径说明、许可、限流、凭证引用 | dataservice |
| raw_objects | 请求指纹、对象键、sha256、received_at | dataservice |
| records_*（按数据集分表）| 统一记录信封字段 + 载荷；record_id+record_version 唯一 | dataservice |
| record_conflicts | 冲突的记录对、差异、处理状态 | dataservice |
| snapshots / snapshot_items | snapshot_hash 唯一；记录引用与选取理由 | dataservice |
| fact_sheets | snapshot_hash + skill_version + formula 版本唯一；事实表对象引用 | harness |
| calculation_runs | formula_id/version、输入引用与哈希、结果、单位、适用性 | harness |
| audit_events | actor、action、resource、result、trace_id；只追加 | platform |

快照和事实表放在共享区，是因为同一数据快照可以被多个用户的任务复用（例如同一交易日对同一股票的个股分析）。用户只能通过自己任务的 `snapshot_hash` 访问它们，读取接口校验任务归属。

## 2. 隔离

- 私有表都有非空 `workspace_id`，外键是包含 `workspace_id` 的复合外键。
- 应用数据库角色不是超级用户，也没有 BYPASSRLS；每个事务开头设置 `app.workspace_id`，私有表启用 RLS。
- Worker 处理研究任务时使用任务所属的 workspace 上下文。定时作业（数据刷新、自选股快照）使用专用角色：只能写共享表，以及按 workspace 逐个写快照。
- 越权读取一律返回 404。

## 3. 版本与数值

- ID 为 UUID；revision、epoch 为 BIGINT，从 1 开始。
- 报告、快照、事实表、统一记录的版本不可原地修改。
- 金额与比例用 NUMERIC，禁止经过 float；未知值为 NULL 并附原因。
- 时间分开存储：`period`（报告期或交易日）、`published_at`、`available_at`、`ingested_at`。

## 4. 事务与竞争

| 动作 | 原子边界 | 崩溃后 |
| --- | --- | --- |
| 确认计划 | 校验幂等键、计划未过期且未被使用、`plan_hash`；预留配额；写任务、首个事件和 River 作业；标记计划已使用 | 同一幂等键返回原结果 |
| 冻结快照 | 先写对象并校验哈希；短事务写 snapshot + items，更新任务 snapshot_hash 与状态 | 未被引用的对象按保留期回收 |
| 领取步骤 | 条件更新 `task_steps`（PENDING 或租约过期，且 epoch 等于任务当前 epoch）| 旧 epoch 的执行无法提交结果 |
| 模型调用 | 发送前写 `model_calls`（SENT）与费用预留；返回后写结果与实际费用 | 有成功记录则复用；SENT 且超时则转 UNKNOWN，按最大费用保留 |
| 取消 | 锁任务；未完成则 epoch+1、状态 CANCEL_REQUESTED，写事件与 `research.cancel` 作业 | 后到的结果只记录费用 |
| 交付 | 锁任务；校验 epoch、计划哈希、快照哈希、必需检查结果、当前权限；写 report_version、publication、任务 COMPLETED、事件；释放配额 | 重复交付返回同一 publication |

默认隔离级别 READ COMMITTED。事务内不等待网络或模型；序列化失败和死锁的重试有次数上限。

## 5. 索引

至少需要：私有表的 `workspace_id + created_at + id`；`research_tasks(status, deadline)`；`task_steps(task_id, epoch, status)`；`model_calls(status, sent_at)`（用于对账）；`records_*` 的 `entity_id + dataset_id + period/trade_date`；`entity_aliases` 的代码与名称三元组（trigram）索引；`watchlist_items(workspace_id)`。

## 6. 留存

私有任务、报告和自选股快照默认保留 90 天（ADR-0001 Q-11）；用户删除任务时同步删除报告和私有结论，审计事件只保留去敏元数据。共享的统一记录和原始响应按厂商许可的期限保留；许可未说明时，PILOT 前由 T014 确认。
