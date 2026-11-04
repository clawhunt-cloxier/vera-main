# 首版数据模型与事务

本文件是逻辑模型与不变量；T002 负责生成可执行 goose SQL/sqlc，并以真实 PostgreSQL 集成测试验证，当前没有声称迁移已经实现。

## 1. 表与所有权

| 表 | 关键字段/唯一性 | 模块 |
| --- | --- | --- |
| users / workspaces / memberships | issuer+subject唯一；首版每用户私人workspace；role来自服务端 | identity |
| sessions | hash(session_id)、actor、workspace、expires_at、csrf_hash、revoked_at | identity |
| source_products / licenses | source/product、原始发布方、用途、有效期、配置版本、secret引用 | sources |
| entities / entity_aliases | entity_id；市场+代码+有效期；行业白名单版本 | governance |
| research_tasks | workspace/id、template/version、period/as_of、status、revision、epoch、deadline、预算、report_ref | research |
| task_events / outbox | event_id唯一；task+sequence唯一；expected_revision；租约、attempt、not_before | research |
| raw_objects / evidence_versions | source、不可变对象键、sha256、locator、origin_group、许可/治理版本 | governance |
| task_datasets / dataset_items | workspace/task/dataset_version唯一；manifest_hash；每条选择理由 | governance |
| context_packs | dataset版本、role、question、content_hash、object_ref | governance |
| operations / operation_attempts | task/epoch/step/input_hash唯一；fencing_token、lease；attempt_id区分真实收费 | agents |
| usage_reservations / usage_ledger | attempt_id+entry_type唯一；预留、实际与未知费用分开；decimal NUMERIC | agents |
| claim_versions / claim_evidence | claim+version；支持/反对/背景；引用确切证据版本 | agents |
| calculation_runs | formula版本、输入引用/hash、结果、单位、适用性 | calculations |
| validation_runs / check_results | 目标hash、规则/模型版本、PASS/FAIL/UNKNOWN、原因和引用 | validation |
| report_versions / delivery_decisions | report/version唯一；冻结正文/claims/hash；自动决定，无审核人 | reports |
| publications | task/epoch/report_version唯一；workspace可读标志；通知状态独立 | delivery |
| feedback | workspace/user、report/version、kind、comment、created_at；幂等关联 | feedback |
| idempotency_keys | workspace+HTTP方法+规范化路由+key唯一；request_hash、status、response_ref、expires_at | platform |
| audit_events | actor/action/resource/version/result/trace；追加写 | platform |

全部私有业务表带非空 workspace_id，复合外键包含 workspace_id；来源共享必须显式许可映射，不能用 NULL workspace 隐式公开。API/Worker 数据库角色不使用超级用户/BYPASSRLS；事务设置并清理 workspace 上下文。后台全局调度使用仅能领取任务的专用角色，不将跨租户读写权限暴露给模型。

## 2. 版本与数值

UUID 稳定逻辑ID，revision/epoch BIGINT >=1；版本记录不可原地改正文。金额和比率 NUMERIC，禁止 float 中转。未知值 NULL+原因，不写0。时段与时点分开；source published_at、ingested_at、available_at 与修订关系完整保存。

原文、PDF、pack、完整报告放对象存储；表保留哈希、检索字段和关系。Evidence URI 是受控逻辑引用；删除受限内容可保留许可允许的审计元数据，不能把不可变误解为无限留存。

## 3. 事务与竞争矩阵

| 动作 | 原子边界 | 崩溃后处理 |
| --- | --- | --- |
| 创建任务 | 验证幂等键，预留任务名额/预算，写任务+启动outbox+响应 | 同键读取原结果；Outbox重发稳定workflow_id |
| 编译数据集 | 对象先写并校验；短事务冻结manifest+items+outbox | 未引用对象按保留期回收；DB提交后不依赖缓存 |
| Operation领取 | 行锁/条件更新revision、epoch与lease，递增fencing | 过期执行不得提交业务结果；仍记录其真实费用 |
| 模型完成 | 保存attempt结果、费用核销事件、operation结果 | 返回Activity前崩溃则重试读结果，不再次调用模型 |
| 取消 | 锁任务；未完成则撤销旧epoch提交资格并写取消事件 | 后到结果只留诊断；不能恢复任务 |
| 交付 | 锁任务；重查epoch、当前权限、hash和全部必需检查；写publication+任务COMPLETED+outbox | 重复返回同一publication；先交付后取消409 |

默认 READ COMMITTED；需要多次读取同一候选集时使用确切不可变版本列表/REPEATABLE READ。串行化失败/死锁重试必须有界，不在事务中等待网络或模型。Outbox 领取租约后提交事务再发送；消费者按 event_id 去重。若检查权限依赖独立系统，调用后还要校验本地策略版本且记录允许的撤权窗口，首版目标60秒。

## 4. 索引与测试

索引至少覆盖 workspace+task.created_at/id，status/not_before，task+epoch，source+published_at，entity/metric/period，dataset+version，claim/report依赖。T002 验证复合FK、唯一约束、RLS、幂等冲突；T006 验证同事务交付/取消和未知费用窗口。
