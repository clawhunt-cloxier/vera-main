# 任务与进度

状态：TODO / IN_PROGRESS / REVIEW / DONE / BLOCKED。DONE 需要可检查产物及测试证据。这里的代码审查不是在线研报审核。时间使用UTC，owner为会话操作者，认领机制见CONTRIBUTING。

| ID | 工作包 | 依赖 | 状态 | owner / 分支 | 验收与范围 |
| --- | --- | --- | --- | --- | --- |
| T000 | 冻结首版规范与协作仓库 | 无 | DONE | Codex / main（授权初始化） | docs、contracts、任务与交接齐全；check-docs |
| T001 | 可运行Web/API起点与CI | T000 | DONE | Codex / main（授权初始化） | healthz/readyz、React起始页、Go测试、前端构建、CI；不宣称业务完成 |
| T002 | 数据库/对象存储/Temporal开发环境与基础模型 | T001 | TODO | 未领取 | goose/sqlc、Compose隔离、真实DB集成、RLS/唯一性；DATA_MODEL |
| T003 | OIDC/会话/私人workspace与多类研究对象查询 | T002 | TODO | 未领取 | CSRF/state/nonce、跨账号隔离、Keycloak本地登录；V01 |
| T004 | 来源接口/治理/冻结数据集/原文定位 | T002 | TODO | 未领取 | fixture先行，EXT-01支持真实接入；DG+通用研究的缺证/修订；无密钥部分可独立完成 |
| T005 | 公式、模型适配、单研究与独立验证 | T004 | TODO | 未领取 | Decimal、Claim/schema、依据不足/错引；EXT-02真实模型后接入；V03/V04 |
| T006 | Temporal任务/Outbox/取消/费用/自动交付 | T003,T005 | TODO | 未领取 | 重放、故障窗口、幂等竞争、预算；V05/V06 |
| T007 | 登录到报告证据与反馈的完整网站 | T003,T006 | TODO | 未领取 | 任务列表/轮询/取消/full/limited/终态、手机布局、Playwright；V07 |
| T008 | 真实来源模型联调与独立质量回归 | T004,T005,T007 | TODO | 未领取 | EXT-01/02/04，30个跨标的研究案例+对抗测试、成本/支持率；V02/V09 |
| T009 | 试用部署、性能、备份恢复与后期上线核查 | T006,T007,T008 | TODO | 未领取 | EXT-03/05、部署锁/迁移/回滚/压测/恢复；V08 |

## 执行规则

每个任务必须拆小提交，先写契约/测试再联通实现。T007可先做不依赖后端的UI组件，但完整验收等待T006；不能提前标DONE。T004/T005使用fixture推进不代表外部依赖已完成。

领取记录例：`T002 — owner=Claude-Code — branch=agent/claude/T002-storage — claimed_at=<UTC> — scope=backend/sql,deployment`，发布到远端后按协作流程集成领取记录。未取得外部条件只标对应子项BLOCKED，可继续其他独立功能。

## 下一步

初始化完成后由下一位Agent领取T002。T003/T004可在T002数据/身份契约稳定后由不同Agent分支执行；禁止同时修改迁移编号或contracts而无协调。

## 本次交接

T000/T001：2026-11-04 20:26 UTC 完成，本地检查证据及范围限制见 [初始化交接](docs/handoffs/2026-11-05-bootstrap-codex.md)。远端 CI 运行状态以 GitHub Actions 为准，不与本地检查混记。

范围修订（REVIEW）：Codex / agent/codex/p0-research-scope，根规范、契约与文案；见 [范围交接](docs/handoffs/2026-11-05-research-scope-codex.md)。T002–T009 按 ADR-0002 执行，年度点评不是唯一研究入口。

## 分支维护

日常领取与集成真值在 dev，任务 PR 目标 dev。分支规范初始化：Codex / agent/codex/branch-policy；范围为协作规范与 CI，详见 docs/BRANCHING.md。现有范围调整分支待以 dev 为目标审查集成。

CI 运行时修订（REVIEW）：Codex / agent/codex/ci-action-runtime；仅升级基础 Action 的 Node 24 运行时，见 [交接](docs/handoffs/2026-11-06-ci-runtime-codex.md)。
