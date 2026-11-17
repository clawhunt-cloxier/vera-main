# T027 集成链路工程审查

- 任务：T027 数据治理实时研报主链路。
- 审查者：Codex `/root/worker_realtime`；本代理实现过 Harness/worker 部分，对该部分属于作者复核，对根代理新增的配置、报告和证据接线作单独交叉审查。共享 GitHub 账号不构成独立审批，也不据此伪造 GitHub approval。
- 被审查 commit：`6ff0c3f`，基线 `0052ea8`。
- 实际读取范围：`backend/internal/harness` 中计划定义、确认意图、任务创建、运行/恢复、模型发送、交付；`emquant` 中采集、发布、冻结清单、授权与交易状态映射；`httpapi` 的协议协商、报告/PDF/series/证据/反馈、RLS 查询和幂等缓存；应用/环境配置及迁移 00019。
- 实际执行检查与结果：本轮只读 diff 和调用链审查，未重复数据库/race 测试；此前真实代码路径的模拟 ABI/S3/PostgreSQL 集成测试与 Harness 包测试见 T027 测试记录，不等价于真实厂商验收。

## 发现

1. P2：实时报告来源列表包含未参与快照的厂商。`harness/report.go:317` 默认读取全部 enabled provider 名称，仅 observation.demo.2 使用特定 provider；因此 demo.3 的 `sources[].provider` 在多个厂商或残留 fixture 的数据库中会写入非实际来源，与 `source_freshness` 和证据的来源不一致。应从固定快照中的 provider ID 生成数据集来源，不依赖全库 enabled 列表。
2. P2：实时报告反馈的副作用先于授权。`httpapi/observation_brief.go:51` 的 `protectBriefRead` 包装 `/reports/` 下所有方法，先执行 `fn(buf,r)` 再核验当前 display 权限；`POST .../feedback` 因此可能已经提交反馈，却在许可撤销/过期后返回失败。建议将该保护限制为 GET 表示读取，反馈继续按 workspace 与已交付 claim 校验执行；若反馈也需要来源权限，则须将核验与写事务放在一起。
3. P2：0.4 能力目录与实际新建版本不一致。`httpapi/handlers.go:252` 的 `/skills` 无论协商结果都从全局 `Engine.Skills` 返回 observation `1.1.0-demo.2`，而 0.4 创建计划使用 `1.1.0-demo.3`。应按协商协议返回 `RealtimeBriefSkill`，保留 observation 客户端的原版本。

## 已核对边界

- `quote_requirement` 由客户端明确提供，持久化列不可变；新哈希绑定该字段，确认在锁定计划后再次重建并核对，模型不能决定行情要求或数据范围。
- 0.4 确认事务将任务、消费计划、意图、数据集要求与 River 作业一起提交；权限拒绝回滚该事务。0.3/observation 确认不会隐式升级为实时技能。
- 冻结恢复使用 manifest 的原始 revision；任务进度 revision 变化不会重采。缺失/损坏清单不会用新行情重建。
- 当前模型发送与报告/证据最终写出前，均验证固定清单、当前许可与 policy，并持有用途锁；HTTP 的系统侧归档授权之前先走 workspace 可见性检查。
- 取消递增 epoch 并锁任务；最终交付再次核对 epoch、状态、计划哈希、冻结哈希和当前 revision。未发现本次新增链路可跨 workspace 或交付已取消旧 epoch 的路径。
- `realtime_trade` 要求已登记实时性质及可证明的连续交易状态；`latest_available` 明示延迟/未知/收盘状态，二者均受登记的最大年龄限制。真实字段语义、状态代码和授权依据仍属于部署验收外部条件，测试 fixture 不能证明厂商语义。

结论：`6ff0c3f` 需修复以上三项再收口；未发现更高严重度的冻结、权限或隔离绕过。该结论只覆盖本次实际读取范围，不替代独立工程审批或真实账号盘中验收。
