# 首版 HTTP API

契约源：[contracts/openapi.json](contracts/openapi.json)（OpenAPI 3.1）；请求结构以 contracts/schemas/ 中的 JSON Schema 2020-12 为准。当前只实现了健康检查，其余是实现目标，见 TASKS。

## 1. 身份与通用行为

- 浏览器使用同源 API `/api/v1`。OIDC Authorization Code + PKCE 由后端完成，会话 cookie 为 HttpOnly、Secure、SameSite=Lax；访问 token 不放入 localStorage。
- 只有开发环境可配置关闭 Secure cookie，生产环境启动时拒绝该配置。写请求校验会话绑定的 CSRF token 与 Origin；token 从 `GET /me` 获取。
- 服务端从会话确定 workspace，客户端不提交 workspace 或租户 ID。访问不属于本 workspace 的资源一律返回 404。
- 业务 JSON 响应带 `request_id`（健康检查和登录重定向除外）。日志不记录报告正文、密钥和 cookie。
- 时间用 RFC3339；十进制数值用字符串；ID 为服务端生成的 UUID。
- ADR-0004 的 OpenAPI 0.4 草案增加计划 `snapshot_mode`、任务/报告 `snapshot_as_of`、证据事件/接收时间与报告 `source_freshness`。`collect_latest` 的计划 `as_of` 是预览时点，任务冻结前 `snapshot_as_of=null`，冻结后与快照不可变绑定；既有请求及任务继续使用 `fixed_legacy` 原语义。草案字段在迁移和服务实现完成前不可作为已提供的功能；客户端和服务端需协调升级，不能要求旧客户端理解新增字段。
- 需要幂等的写请求带 `Idempotency-Key`（1–128 字符）。同一 workspace、方法、规范化路由和键：请求哈希相同则返回原响应，不同则返回 409。幂等记录保存 30 天。
- 列表：`page_size` 默认 20、上限 100；不透明游标，按 `created_at` + `id` 稳定排序。

## 2. 路由

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET | /healthz | 进程存活 |
| GET | /readyz | 检查配置、数据库与 River 可用；不检查模型与厂商 |
| GET | /auth/login、/auth/callback | 登录与回调；state、nonce、PKCE；防开放重定向；首次登录需有效邀请码 |
| POST | /auth/logout | 销毁会话 |
| GET | /api/v1/me | 用户、workspace、CSRF token、配额剩余 |
| GET | /api/v1/skills | 技能列表、版本、参数 Schema、当前可用性 |
| GET | /api/v1/entities | `q` 搜索；`kind` 过滤 stock/industry/market；返回各技能可用性 |
| POST | /api/v1/research-plans | 一句话或结构化输入生成计划；返回计划或候选列表；不预留配额 |
| GET | /api/v1/research-plans/{id} | 读取未过期计划 |
| POST | /api/v1/research-tasks | 用 `plan_id` + `plan_hash` 确认计划并创建任务，幂等，202 |
| GET | /api/v1/research-tasks | 任务列表，可按技能、状态、对象过滤 |
| GET | /api/v1/research-tasks/{id} | 状态、revision、步骤进度、原因、`report_ref` |
| POST | /api/v1/research-tasks/{id}/cancel | 校验 `expected_revision`；202 取消处理中 / 200 已取消 / 409 已完成 |
| GET | /api/v1/reports | 报告列表，可按对象、技能过滤 |
| GET | /api/v1/reports/{id}/versions/{version} | 已交付的报告：抬头、章节、结论、表格、图表数据、限制 |
| GET | /api/v1/reports/{id}/versions/{version}/series/{series_id} | 报告图表数据，来自该报告冻结的数据快照，不取实时数据 |
| GET | /api/v1/evidence/{id} | 证据版本：来源、口径、时点、原文片段或数值；不返回可公开访问的永久 URL |
| GET | /api/v1/calculations/{id} | 公式、版本、输入引用、结果 |
| POST | /api/v1/reports/{id}/versions/{version}/feedback | 反馈：useful/confusing/incorrect/other，可关联 claim；备注 ≤ 2000 字；幂等，201 |
| GET / POST | /api/v1/watchlist | 列表 / 添加 |
| PATCH / DELETE | /api/v1/watchlist/{item_id} | 修改标签、备注、提醒 / 移除 |

轮询：任务详情响应带 `Retry-After`，执行中建议 3 秒，可退避到 15 秒，终态后停止。首版不提供 SSE。

## 3. 错误

错误信封：`{request_id, error: {code, message, retryable, details}}`。

| 状态码 | 用途 |
| --- | --- |
| 400 | 参数格式错误 |
| 401 | 未登录 |
| 403 | CSRF 或 Origin 校验失败 |
| 404 | 资源不存在或不可见 |
| 409 | 幂等冲突、`plan_hash` 不匹配、计划过期、revision 冲突、`ALREADY_COMPLETED` |
| 422 | 不支持的需求或对象（`UNSUPPORTED_REQUEST`、`UNSUPPORTED_SUBJECT`、`CAPABILITY_UNAVAILABLE`）|
| 429 | 配额或限流 |
| 503 | 依赖不可用 |

任务创建成功后发生的缺数据、预算耗尽等情况通过任务终态表达，不返回 HTTP 5xx。

任务终态：COMPLETED、ABSTAINED、BUDGET_EXHAUSTED、CANCELLED、BLOCKED、FAILED。`result_kind`（full/limited）只出现在已交付的报告上。客户端不能提交或修改核验结果。

## 4. 变更

修改契约时同步更新 OpenAPI、JSON Schema、Go DTO、前端类型和契约测试。前端类型由 OpenAPI 生成（T013 选定生成器并锁定版本），不手写重复类型。删除或改名字段需要兼容窗口；已冻结的报告不随 schema 变化而改写。

## 5. 浏览器投影

OpenAPI 中的 Claim、Evidence 是报告页的读取投影，不是模型的输出结构；模型输出结构见 [HARNESS](docs/HARNESS.md) §6.2。浏览器不能回传或修改 Claim。证据的来源等级、口径和原文位置由数据服务写入，展示时不由模型补充。
