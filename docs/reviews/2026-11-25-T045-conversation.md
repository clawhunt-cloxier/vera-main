# T045 对话研究工具工程审查

- 审查者为独立代理 `contract_review`，被审查提交为 `3e8078a57ac4dabd5a37b85185b72c79666e1d60`，对照 `dev@53f7f4e`。本记录由实现者按审查者回报整理。
- 审查覆盖 OpenAPI、模型契约与生成的 TypeScript 类型、旧计划确认和自动研究的 River 事务边界、标的和窗口核验、直接回复及禁用表达检查、路由调用留痕和计费对账、迁移 `00026` 至 `00028` 和密码登录。
- 审查者复核了具体修复。用户在引用历史研究时提出新时间范围，服务会要求澄清。交易日窗口校验覆盖全角数字。异常模型响应保留已知费用，未知用量留待对账。密码与 OIDC 登录共用的站内跳转校验会拒绝控制字符。
- 审查者在最终实现提交运行了 `git diff --check 53f7f4e..3e8078a`、定向 Go 回归 `TestSafeReturn` 和 `TestConversationTradingDayScope`、`make check-docs`、`make check-contracts`、`CONTRACT_BASE_REF=53f7f4e make check-contract-compat` 和 `cd web && npm run check:api`，均通过。兼容性只提示 `GET /api/v1/conversations/{conversation}` 的轮次状态新增 `answer`。穷举状态的客户端需要处理新增值。
- 审查者核对了本地服务的提交信息、`/readyz` 和 `/app`。完整 race 和浏览器检查由实现者运行。见 [T045 交接](../handoffs/2026-11-25-T045-codex.md)。
- 后续复核覆盖 `3dd7e44e88166a58ee0a73bf8f3728703bd91fcd`。用户写出带市场后缀的未登记代码而模型只摘取数字时，服务要求补充，当前轮次不绑定旧报告或创建研究任务。登记的不同标的仍会触发澄清。模型填入用户原文没有的名称时按已绑定轮次引用。文档与差异检查通过，无新增阻止合入的发现。
- 合并前需完成用户浏览器验收与当前 PR 检查。本机模型路由检查结果见交接。正式模型、授权数据及生产运行成本待对应环境验收。
