# 首版数据模型与事务

状态：依 ADR-0003 修订的逻辑模型与不变量。正式 goose 迁移位于 `backend/internal/platform/db/migrations/`；下表是逻辑模型，不表示全部数据集已覆盖。数据服务的统一记录字段见 [DATA_SERVICE](docs/DATA_SERVICE.md) §4，Harness 的步骤与结论结构见 [HARNESS](docs/HARNESS.md)。

## 1. 表与所有权

### 1.1 用户私有（带非空 workspace_id）

| 表 | 关键字段与唯一性 | 模块 |
| --- | --- | --- |
| users | issuer+subject 唯一；persona；invited_by | identity |
| workspaces | 每用户一个；owner_user_id 唯一 | identity |
| sessions | hash(session_id)、user、workspace、expires_at、csrf_hash、revoked_at | identity |
| research_plans | id、skill_id/version、subject_id、params、as_of、snapshot_mode、quote_requirement、sections、plan_hash、expires_at、consumed_task_id | harness |
| research_tasks | id、plan_id 唯一、skill_id/version、subject_id、status、revision、epoch、deadline、budget、snapshot_mode、snapshot_hash、snapshot_as_of、snapshot_manifest_hash、report_ref、reason_code | research |
| quote_task_intents / quote_task_components | workspace + task + epoch/revision；已确认行情意图及候选组件 | dataservice |
| task_snapshot_requirements / task_snapshot_manifests | workspace + task；不可变要求、冻结截止、行情组件及哈希 | dataservice |
| observation_brief_snapshots | workspace + task；旧 observation 冻结清单 | dataservice |
| task_events | task+sequence 唯一；event_type、expected_revision、epoch、payload | research |
| task_steps | task+epoch+step_key+input_hash 唯一；status、lease_until、attempt、output_ref、output_hash | harness |
| model_calls | step_id+attempt 唯一；profile、model_id、observed_model_version、request_hash、状态（SENT/SUCCEEDED/FAILED/UNKNOWN）、usage、cost、reserved_cost | harness |
| usage_ledger | task_id+entry_id 唯一；entry_type（reserve/actual/release/unknown_hold）、amount（NUMERIC）、price_table_version | research |
| claims | task+epoch+section+claim_ref 唯一；kind、text、refs、numbers、verdict、removed_reason | harness |
| report_versions | report_id+version 唯一；task_id 唯一；冻结的渲染 JSON 引用与哈希、snapshot_hash、plan_hash、skill/rule/prompt 版本 | reports |
| publications | task_id+epoch 唯一；report_version、delivered_at | reports |
| watchlist_items | workspace+entity 唯一；tags、note、added_at、added_price、added_price_provider_id、version | watchlist |
| watchlist_alerts | item+kind 唯一；threshold、enabled、last_triggered_trade_date | watchlist |
| watchlist_snapshots | item+provider_id+trade_date 唯一；指标值、triggered。来源为空的历史快照保留到期，读取时仅返回当前厂商已归属的行 | watchlist |
| feedback | report+version+user+idempotency_key 唯一；kind、claim_ref、comment | feedback |
| idempotency_keys | workspace+方法+规范化路由+key 唯一；request_hash、response_ref、expires_at | platform |

### 1.2 平台共享（无 workspace_id，用户角色只读）

| 表 | 内容 | 模块 |
| --- | --- | --- |
| invites | 邀请码哈希、额度、过期时间、创建者 | identity |
| entities / entity_aliases | 实体、类型、代码、名称、拼音、曾用名、生效区间 | catalog |
| tushare_requested_subjects | 以证券代码关联实体，记录该股按需完成 TuShare 数据发布的时间。与正式数据同事务提交，供各进程判断研究准入及是否需要重新采集。 | dataservice |
| capabilities | 数据集 × 厂商 × 市场的启用状态、覆盖区间、许可用途、更新时间 | catalog |
| providers / provider_datasets | 厂商与产品、口径说明、许可、限流、凭证引用 | dataservice |
| raw_objects | 请求指纹、对象键、sha256、received_at | dataservice |
| records_*（按数据集分表）| 统一记录信封字段 + 载荷；record_id+record_version 唯一 | dataservice |
| record_conflicts | 冲突的记录对、差异、处理状态 | dataservice |
| snapshots / snapshot_items | snapshot_hash 唯一；记录引用与选取理由 | dataservice |
| fact_sheets | snapshot_hash + skill_version + formula 版本唯一；事实表对象引用 | harness |
| calculation_runs | formula_id/version、输入引用与哈希、结果、单位、适用性 | harness |
| audit_events | actor、action、resource、result、trace_id；只追加 | platform |

旧统一层快照和事实表可被多个用户的任务复用，用户只能通过自己任务的 `snapshot_hash` 访问。新单股路径的 `quote_task_intents`、`task_snapshot_requirements`、`task_snapshot_manifests` 及 observation 清单按 workspace 与任务绑定；底层归档和已发布行情候选共享。读取接口始终校验任务归属。

迁移 `00015` 已加入 ADR-0004 的 `snapshot_mode`、`snapshot_as_of` 与清单哈希约束，`00016` 提供任务要求和不可变快照清单，T027 的 `00019` 保存不可变 `quote_requirement`；单股 demo.3 已接通确认和冻结。`collect_latest` 的任务在冻结前截止为空；冻结事务必须同时固定截止和快照哈希，并用 epoch 与唯一约束保证并发重试只能采用同一结果。既有任务按 `fixed_legacy` 保留原时间及快照，不得重算。新增数据及血缘的 `available_at` 必须不早于其真实持久化完成；跨 S3 与 PostgreSQL 的提交顺序、崩溃恢复及可见性由实施迁移与集成测试证明，不能仅用对象接收时间代替数据库可见时间。

对话涉及三张私有表。迁移 `00023` 创建 `conversations`（标题、结构化范围、revision、活动时点与保留期）、`conversation_turns`（消息 UUID、原文、解析状态和计划/任务引用）及 `conversation_quota_debits`（当日删除的短任务配额记录）。`research_plans` 区分会话计划和会话短研究。所有关联按 workspace 校验，回复外键同时约束会话。消息接收与 River 入队同时提交，确认时在同一事务锁定会话及轮次、预留配额、绑定任务。数据结果由原有快照、事实表与报告版本保存。

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
| 冻结快照 | 原始对象发布后，在固定 MVCC 视图按当前 task epoch/revision 选取并绑定不可变清单、snapshot_as_of 与清单哈希；旧统一层保留 snapshot + items | 重试读取原清单；未被引用对象的回收策略仍须实现 |
| 领取步骤 | 条件更新 `task_steps`（PENDING 或租约过期，且 epoch 等于任务当前 epoch）| 旧 epoch 的执行无法提交结果 |
| 模型调用 | 发送前写 `model_calls`（SENT）与费用预留；返回后写结果与实际费用 | 有成功记录则复用；SENT 且超时则转 UNKNOWN，按最大费用保留 |
| 取消 | 锁任务；未完成则 epoch+1、状态 CANCEL_REQUESTED，写事件与 `research.cancel` 作业 | 后到的结果只记录费用 |
| 交付 | 锁任务；校验 epoch、计划哈希、快照哈希、必需检查结果、当前权限；写 report_version、publication、任务 COMPLETED、事件；释放配额 | 重复交付返回同一 publication |

默认隔离级别 READ COMMITTED；实时发布选择、冻结和绑定快照读取使用 REPEATABLE READ。一般事务内不等待网络或模型；实时快照在原始对象核验、模型发送和最终响应写出期间保留用途授权锁，以保证使用时许可有效，这些路径有调用超时并在使用后释放事务。序列化失败和死锁的重试有次数上限。

## 5. 索引

至少需要：私有表的 `workspace_id + created_at + id`；`research_tasks(status, deadline)`；`task_steps(task_id, epoch, status)`；`model_calls(status, sent_at)`（用于对账）；`records_*` 的 `entity_id + dataset_id + period/trade_date`；`entity_aliases` 的代码与名称三元组（trigram）索引；`watchlist_items(workspace_id)`。

## 6. 留存

对话自最后一次修改起保存 90 天。删除或过期后立即从读取接口隐藏。清理任务擦除对话文本与派生范围，取消在途任务，移除短报告、专有媒体快照和计算等工作区产物。迁移 `00025` 记录模型回复及错误详情的擦除时间。晚到的调用只能结算用量和费用，新调用必须匹配仍在执行的任务 epoch。已交付的正式研报、厂商共享记录与计费审计按原有约定保留。清理产生的当日配额记录在配额周期结束后删除。

私有任务、报告和自选股快照默认保留 90 天（ADR-0001 Q-11）；用户删除任务时同步删除报告和私有结论，审计事件只保留去敏元数据。共享的统一记录和原始响应按厂商许可的期限保留；许可未说明时，PILOT 前由 T014 确认。
