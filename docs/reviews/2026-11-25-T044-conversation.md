# T044 对话研究工程审查

- 审查者为 Codex 独立审查代理 `contract_review`，被审查提交为 `0ebdcc3c5c2cc9b08c753cec018ed3a15a10b150`，对照 `dev@b8453ba`。
- 审查了会话删除与报告交付的锁顺序、私有证据权限、三项迁移、Choice 预算和旧 Worker 兼容性，以及清理后模型调用的计费对账。
- 审查者执行 `git diff --check b8453ba 0ebdcc3` 和 `python3 scripts/check_contract_compat.py --base b8453ba`，均通过。PostgreSQL、River 与 Chromium 验收由实现者运行，见 [T044 交接](../handoffs/2026-11-25-T044-codex.md)。
- 复核确认，清理事务擦除短研究的模型回复与错误详情，后续结算仍可保留用量和费用。旧 epoch 不能复用已清理内容或创建新调用。短研究的截止时限也覆盖最终交付。
- 该实现提交无阻止合入的发现。模拟 Choice 验收覆盖归档与读取流程，真实授权及费用实测由发布环境执行。
