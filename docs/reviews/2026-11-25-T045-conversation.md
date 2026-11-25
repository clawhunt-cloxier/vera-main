# T045 对话研究工具工程审查

- 审查者为独立代理 `contract_review`，被审查提交为 `3e8078a57ac4dabd5a37b85185b72c79666e1d60`，对照 `dev@53f7f4e`。本记录由实现者按审查者回报整理。
- 审查覆盖 OpenAPI、模型契约与生成的 TypeScript 类型、旧计划确认和自动研究的 River 事务边界、标的和窗口核验、直接回复及禁用表达检查、路由调用留痕和计费对账、迁移 `00026` 至 `00028` 和密码登录。
- 审查者复核了具体修复。用户在引用历史研究时提出新时间范围，服务会要求澄清。交易日窗口校验覆盖全角数字。异常模型响应保留已知费用，未知用量留待对账。密码与 OIDC 登录共用的站内跳转校验会拒绝控制字符。
- 审查者在最终实现提交运行了 `git diff --check 53f7f4e..3e8078a`、定向 Go 回归 `TestSafeReturn` 和 `TestConversationTradingDayScope`、`make check-docs`、`make check-contracts`、`CONTRACT_BASE_REF=53f7f4e make check-contract-compat` 和 `cd web && npm run check:api`，均通过。兼容性只提示 `GET /api/v1/conversations/{conversation}` 的轮次状态新增 `answer`。穷举状态的客户端需要处理新增值。
- 审查者核对了本地服务的提交信息、`/readyz` 和 `/app`。完整 race 和浏览器检查由实现者运行。见 [T045 交接](../handoffs/2026-11-25-T045-codex.md)。
- 审查通过。合并前需完成用户浏览器验收与 PR 检查。真实模型路由、真实数据及厂商成本留待对应环境验收。
