# 任务与进度

状态：TODO / IN_PROGRESS / REVIEW / DONE / BLOCKED。DONE 需要可检查的产物和测试证据。这里的 REVIEW 指工程代码审查，不是研报审核。时间用 UTC，领取方式见 CONTRIBUTING。

ADR-0003 起，原 T002–T009 作废（均未领取），由下表 T011–T024 取代。

| ID | 工作包 | 依赖 | 状态 | owner / 分支 | 验收与范围 |
| --- | --- | --- | --- | --- | --- |
| T000 | 冻结首版规范与协作仓库 | 无 | DONE | Codex / main（授权初始化） | docs、contracts、任务与交接；check-docs |
| T001 | 可运行 Web/API 起点与 CI | T000 | DONE | Codex / main（授权初始化） | healthz/readyz、React 起始页、Go 测试、前端构建、CI |
| T010 | 产品模块、Harness、数据服务与运行时规范（ADR-0003） | T001 | REVIEW | Claude-Code / agent/claude/T010-product-architecture | SPEC、ARCHITECTURE、HARNESS、DATA_SERVICE、技能规格、API/契约、DATA_MODEL、TASKS；check-docs、check-contracts |
| T011 | 工程门禁 | T010 | REVIEW | Claude-Code / agent/claude/T011-contract-gates | golangci-lint v2（`make lint-go`，CI 固定 v2.15.0）、前端 ESLint/Prettier（`make lint-web`）、CODEOWNERS、工具链版本检查（`make check-toolchain`）、OpenAPI→TS 漂移检查（`make check-api-types`）、契约兼容检查（`make check-contract-compat`），均已接入 `make verify`，见 [交接](docs/handoffs/2026-11-12-T011-claude.md) |
| T012 | 开发环境与数据库基础 | T010 | IN_PROGRESS | Claude-Code / agent/claude/T012-dev-compose | Compose（postgres、对象存储、keycloak、caddy）、goose/sqlc、RLS、River 接入、`make dev-up`、`make test-integration`；DATA_MODEL §1–4 的集成测试；OPERATIONS §1 版本锁定；领取于 2026-11-12T08:08:50Z，计划与差距见 [盘点](docs/handoffs/2026-11-12-T012-T024-plan-claude.md) |
| T013 | 身份、会话、邀请与 workspace | T012 | IN_PROGRESS | Claude-Code / agent/claude/T013-identity-gaps | OIDC（自建 Keycloak，realm 入库）、会话、CSRF、邀请码、onboarding、`/me`；V01；领取于 2026-11-12T08:08:50Z，计划与差距见 [盘点](docs/handoffs/2026-11-12-T012-T024-plan-claude.md) |
| T014 | 数据服务框架与 fixture 适配器 | T012 | DONE | Claude-Code / agent/claude/T014-data-framework | Provider/Mapper 接口、映射 YAML、按 contracts/datasets/catalog.json 的统一记录、快照、新鲜度、对账；按 demo-universe 生成 fixture；production 拒绝 fixture；见 [T014 交接](docs/handoffs/2026-11-14-T014-claude.md) |
| T014b | 真实厂商适配器 | T014、EXT-01 | IN_PROGRESS | Codex / agent/codex/T014b-emquant | 2026-11-15T04:23:07Z 继续领取；范围：Choice SDK、数据映射、请求指纹和血缘持久化、数据服务集成与真实数据验收；2026-11-15 用户授权先交付单股简版真实数据研报演示，范围包括报告链路、前端和测试环境构建，见 [ADR-0005](docs/decisions/0005-real-data-demo.md)。可能涉及数据库迁移及相关契约，需与其他任务协调。数据授权、字段口径和 PIT 等未确认项保持受限；V02 尚未完成。见 [交接](docs/handoffs/2026-11-12-emquant-probe-codex.md) |
| T015 | 实体目录与能力登记 | T014 | IN_PROGRESS | Claude-Code / agent/claude/T015-entities | 实体搜索、申万分类、能力登记、`/entities`、`/skills`、章节可用性；领取于 2026-11-12T08:08:50Z，计划与差距见 [盘点](docs/handoffs/2026-11-12-T012-T024-plan-claude.md) |
| T016 | 公式库与事实表 | T014 | IN_PROGRESS | Claude-Code / agent/claude/T016-formulas | F01–F25、适用条件、Decimal；FactSheet 生成与稳定 ID；领取于 2026-11-12T08:08:50Z，计划与差距见 [盘点](docs/handoffs/2026-11-12-T012-T024-plan-claude.md) |
| T017 | 模型网关 | T012 | IN_PROGRESS | Claude-Code / agent/claude/T017-model-gateway | OpenAI-compatible 客户端、profile 配置、价格表与高峰时段、usage 记录、录制回放适配器、UNKNOWN 对账；真实调用需 EXT-02；领取于 2026-11-12T08:08:50Z，计划与差距见 [盘点](docs/handoffs/2026-11-12-T012-T024-plan-claude.md) |
| T018 | Harness 核心 | T016、T017 | IN_PROGRESS | Claude-Code / agent/claude/T018-harness | 技能注册、计划（规则解析 + router）、步骤台账、写作、R-01–R-09、语义核验、组装、任务状态机、River 作业、取消、截止时限、交付；V04、V05、V06；领取于 2026-11-12T08:08:50Z，计划与差距见 [盘点](docs/handoffs/2026-11-12-T012-T024-plan-claude.md) |
| T019a | 个股分析与资金动向技能 | T018 | IN_PROGRESS | Claude-Code / agent/claude/T019a-skills | stock_analysis、capital_flow 的章节、提示词、规则；demo-universe 中 SA、CF 用例端到端；replay 录制；V03（部分）；领取于 2026-11-12T08:08:50Z，计划与差距见 [盘点](docs/handoffs/2026-11-12-T012-T024-plan-claude.md) |
| T019b | 深度研究技能 | T019a | IN_PROGRESS | Claude-Code / agent/claude/T019b-deep-research | deep_research 三种模式；文本选证；DR 用例端到端；V03；领取于 2026-11-12T08:08:50Z，计划与差距见 [盘点](docs/handoffs/2026-11-12-T012-T024-plan-claude.md) |
| T020 | 评测与模型对照 | T019a | IN_PROGRESS | Claude-Code / agent/claude/T020-eval | `vera-harness eval`、评测集格式、人工核查表、Flash/Pro 对照报告；真实运行需 EXT-01、EXT-02、EXT-04；V09；领取于 2026-11-12T08:08:50Z，计划与差距见 [盘点](docs/handoffs/2026-11-12-T012-T024-plan-claude.md) |
| T021 | 自选股 | T015、T016 | IN_PROGRESS | Claude-Code / agent/claude/T021-watchlist | 增删改、提醒、收盘快照作业、研究历史；领取于 2026-11-12T08:08:50Z，计划与差距见 [盘点](docs/handoffs/2026-11-12-T012-T024-plan-claude.md) |
| T022 | 网站 | T013、T018 | IN_PROGRESS | Claude-Code / agent/claude/T022-web | 按 docs/WEB.md；M1 先做首页、计划确认、进度、报告与证据侧栏、反馈；M2 加自选股；Playwright；V07。可先用 mock API 开发；领取于 2026-11-12T08:08:50Z，计划与差距见 [盘点](docs/handoffs/2026-11-12-T012-T024-plan-claude.md) |
| T024 | 管理 CLI | T013、T017 | IN_PROGRESS | Claude-Code / agent/claude/T024-admin | vera-admin 全部命令（OPERATIONS §4），写审计事件；领取于 2026-11-12T08:08:50Z，计划与差距见 [盘点](docs/handoffs/2026-11-12-T012-T024-plan-claude.md) |
| T025 | MVP 前后端、PDF 导出与测试环境 CD | T010 | REVIEW | Claude-Code / agent/claude/mvp | README-MVP、[MVP 交接](docs/handoffs/2026-11-07-mvp-claude.md)、[DEPLOYMENT](docs/DEPLOYMENT.md)；PDF 为服务器端排版；真实数据厂商与 T011–T024 的完整实现仍未完成 |
| T026 | 盘中行情与最新可用数据规格 | T001 | IN_PROGRESS | Codex / agent/codex/T026-intraday-contract | 领取于 2026-11-12T05:39:22Z（PR #16 已合入）；2026-11-13T03:08:01Z 在新分支继续；范围：根规格、DATA_SERVICE、相关技能规格及评测样例、数据与 API 契约；依据用户要求记录行情实时性、各数据实际时点、过期降级与权限，不代表实现完成。 |
| T023 | test 环境部署、性能与恢复 | T019a、T022、T024 | TODO | 未领取 | 镜像、Compose 生产配置、Caddy/TLS、备份恢复演练、压测、告警、费用总上限开关；EXT-03；V08 |

## 里程碑

M1 本地演示：T011–T018、T019a、T022（M1 范围）、T024。M2 测试环境：T014b、T019b、T021、T022、T023。M3 PILOT：T020。详见 ADR-0003 D12。

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
- CI 运行时修订：Codex / agent/codex/ci-action-runtime（PR #3 已合入 dev）；基础 Action 升级到 Node 24 运行时，dev 合并后自动清理任务分支，见 [交接](docs/handoffs/2026-11-06-ci-runtime-codex.md)。
- T010：[产品与架构交接](docs/handoffs/2026-11-06-T010-claude.md)。
