# 任务与进度

状态：TODO / IN_PROGRESS / REVIEW / DONE / BLOCKED。DONE 需要可检查的产物和测试证据。这里的 REVIEW 指工程代码审查，不是研报审核。时间用 UTC，领取方式见 CONTRIBUTING。

ADR-0003 起，原 T002–T009 作废（均未领取），由下表 T011–T023 取代。

| ID | 工作包 | 依赖 | 状态 | owner / 分支 | 验收与范围 |
| --- | --- | --- | --- | --- | --- |
| T000 | 冻结首版规范与协作仓库 | 无 | DONE | Codex / main（授权初始化） | docs、contracts、任务与交接；check-docs |
| T001 | 可运行 Web/API 起点与 CI | T000 | DONE | Codex / main（授权初始化） | healthz/readyz、React 起始页、Go 测试、前端构建、CI |
| T010 | 产品模块、Harness、数据服务与运行时规范（ADR-0003） | T001 | REVIEW | Claude-Code / agent/claude/T010-product-architecture | SPEC、ARCHITECTURE、HARNESS、DATA_SERVICE、技能规格、API/契约、DATA_MODEL、TASKS；check-docs、check-contracts |
| T011 | 工程门禁 | T010 | TODO | 未领取 | golangci-lint、ESLint/Prettier、OpenAPI→TS 生成与漂移检查、契约兼容检查、CODEOWNERS；CI 接入 |
| T012 | 开发环境与数据库基础 | T010 | TODO | 未领取 | Compose（postgres、minio、dex）、goose/sqlc、RLS、River 接入、`make dev-up`；DATA_MODEL §1–4 的集成测试 |
| T013 | 身份、会话、邀请与 workspace | T012 | TODO | 未领取 | OIDC（Dex）、会话、CSRF、邀请码、`/me`；V01 |
| T014 | 数据服务框架与 fixture 适配器 | T012 | TODO | 未领取 | Provider/Mapper 接口、映射 YAML、统一记录、快照、新鲜度、对账；覆盖 DATA_SERVICE §3 的 fixture 数据集；production 拒绝 fixture |
| T014b | 真实厂商适配器 | T014、EXT-01 | BLOCKED | 未领取 | 等待用户提供厂商资料；每个映射附样本测试；V02 |
| T015 | 实体目录与能力登记 | T014 | TODO | 未领取 | 实体搜索、申万分类、能力登记、`/entities`、`/skills`、章节可用性 |
| T016 | 公式库与事实表 | T014 | TODO | 未领取 | F01–F25、适用条件、Decimal；FactSheet 生成与稳定 ID |
| T017 | 模型网关 | T012 | TODO | 未领取 | OpenAI-compatible 客户端、profile 配置、价格表与高峰时段、usage 记录、录制回放适配器、UNKNOWN 对账；真实调用需 EXT-02 |
| T018 | Harness 核心 | T016、T017 | TODO | 未领取 | 技能注册、计划（规则解析 + router）、步骤台账、写作、R-01–R-09、语义核验、组装、任务状态机、River 作业、取消、截止时限、交付；V04、V05、V06 |
| T019 | 三项研究技能 | T018 | TODO | 未领取 | stock_analysis、capital_flow、deep_research 的章节、提示词、规则；fixture 端到端；V03 |
| T020 | 评测与模型对照 | T019 | TODO | 未领取 | `vera-harness eval`、评测集格式、人工核查表、Flash/Pro 对照报告；真实运行需 EXT-01、EXT-02、EXT-04；V09 |
| T021 | 自选股 | T015、T016 | TODO | 未领取 | 增删改、提醒、收盘快照作业、研究历史 |
| T022 | 网站 | T013、T018 | TODO | 未领取 | workspace 首页、一句话入口、计划确认、进度、报告与证据侧栏、图表、自选股、反馈、任务与报告列表、手机布局；Playwright；V07。可先用 mock API 开发 |
| T023 | 部署、性能与恢复 | T019、T022 | TODO | 未领取 | 镜像、Compose 生产配置、Caddy/TLS、备份恢复演练、压测、告警、费用总上限开关；EXT-03；V08 |

## 并行建议

- T011、T012 可以同时开始。
- T012 完成后，T013、T014、T017 可由不同 Agent 并行。
- T022 前端可在 T010 契约冻结后用 mock API 先行，完整验收等 T018。
- T016 与 T017 互不依赖。

迁移编号和 contracts/ 由同一时刻一个任务修改；需要改契约时先在任务记录中声明。

## 执行规则

每个任务拆成小提交，先写契约和测试再接通实现。使用 fixture 推进不代表外部依赖已完成。

领取记录示例：`T012 — owner=Claude-Code — branch=agent/claude/T012-dev-env — claimed_at=<UTC> — scope=deploy/compose,backend/migrations`。记录发布到远端后才对其他协作者可见。缺少外部条件时只把对应子项标为 BLOCKED，继续做其他独立部分。

## 交接记录

- T000/T001：[初始化交接](docs/handoffs/2026-11-05-bootstrap-codex.md)。
- 范围修订（ADR-0002）：[范围交接](docs/handoffs/2026-11-05-research-scope-codex.md)；已被 ADR-0003 部分取代。
- 分支规范：[BRANCHING](docs/BRANCHING.md)，Codex / agent/codex/branch-policy。
- T010：[产品与架构交接](docs/handoffs/2026-11-06-T010-claude.md)。
