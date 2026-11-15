# T014b 接手审查与状态映射收口

- **操作者**：Codex/Astra
- **分支**：`agent/codex/T014b-emquant`
- **时间**：2026-11-15 UTC
- **基线**：PR #17 `ac942b7`；审查并吸收状态映射 WIP `cfd8d55`

## 本轮完成

- 审查状态映射 WIP 及其交接记录，将受控交易状态字段、证据引用和类型化值映射贯穿行情采集、SDK 解码归档、发布、固定快照、重新授权和简版确认。
- 交易状态政策只允许大写字段名；状态值必须使用 `s:`, `i:`, `u:` 类型前缀。整数映射要求规范十进制表示（拒绝 `i:01`、`i:+1` 等无法匹配解码值的配置）。未映射、空值和未知值保持 `unknown`。
- 新增合成端到端测试，覆盖三字段授权、状态映射、实时快照可用性、模型/展示用途授权、固定快照在新行情发布后不变、重复发布幂等和政策撤销后的读取拒绝。测试数据不代表 Choice 字段口径或账户权限。
- 修复模型网关瞬时错误重试：退避等待改为可被 `context.Context` 取消，任务取消或截止时不会继续无意义等待；新增等待和网关重试测试。

## 验证

以下命令在 FullAccess 环境通过：

```text
PATH=/home/cloxier/.local/toolchains/go1.27.3/bin:$PATH GOCACHE=/tmp/vera-gocache GOFLAGS=-p=1 go test -race ./...
python3 scripts/check_docs.py
```

全量竞态测试覆盖临时 PostgreSQL、HTTP 对象存储、迁移、Harness、HTTP API、身份和 EMQuant 包，全部通过。状态映射专项测试也单独通过。

## 仍未完成

- Choice `TIME` 的正式时区/事件含义、交易状态字段是否可由 `csqsnapshot` 同时返回、状态编码与实时权限仍需厂商资料或受控账号实测；当前代码不会凭字段存在推断实时。
- 0.4 简版计划 API、任务确认与 River worker 尚未接入现有 HTTP 研究路径；`RegisterBriefIntent`、`FreezeLatestSnapshot` 和 `BoundQuoteFacts` 仍是受信调用组件。
- EMQuant runtime 尚未注册为 `dataservice.Provider`，真实对象存储、Choice 凭证、产品授权和 DeepSeek 配置仍需测试环境接线。
- 证据 API、报告交付、前端简版流程和真实交易时段端到端验收仍未完成；PR 继续保持 Draft。

状态映射 WIP 的 `docs/handoffs/2026-11-15-T014b-status-wip-codex.md` 保留为来源记录；其“不得直接合并”的结论适用于原 WIP 分支，不能代替本轮集成后的测试证据。
