# 首版 HTTP API

契约源：[contracts/openapi.json](contracts/openapi.json)（OpenAPI 3.1）；请求结构以 contracts/schemas/ 中的 JSON Schema 2020-12 为准。当前实现包含工作区研究、报告、证据、自选股及简报协议；具体功能覆盖和真实验收状态见 TASKS。

## 1. 身份与通用行为

- 浏览器使用同源 API `/api/v1`。OIDC Authorization Code + PKCE 由后端完成，会话 cookie 为 HttpOnly、Secure、SameSite=Lax；访问 token 不放入 localStorage。
- 只有开发环境可配置关闭 Secure cookie，生产环境启动时拒绝该配置。写请求校验会话绑定的 CSRF token 与 Origin；token 从 `GET /me` 获取。
- 服务端从会话确定 workspace，客户端不提交 workspace 或租户 ID。访问不属于本 workspace 的资源一律返回 404。
- 业务 JSON 响应带 `request_id`（健康检查和登录重定向除外）。日志不记录报告正文、密钥和 cookie。
- 时间用 RFC3339；十进制数值用字符串；ID 为服务端生成的 UUID。
- 现行 [OpenAPI 0.4 契约](contracts/openapi.json)已增加计划 `snapshot_mode`、任务/报告 `snapshot_as_of`、证据事件/接收时间与报告 `source_freshness`，并保留 0.3 与 observation.v1 的各自表示。[早期目标草案](contracts/drafts/openapi-0.4.json)保留设计背景，当前实现以主契约为准。`collect_latest` 的计划 `as_of` 是预览时点，任务冻结前 `snapshot_as_of=null`，冻结后与快照不可变绑定；既有请求及任务继续使用 `fixed_legacy` 原语义。
- 版本协商协议：业务 JSON 接口请求 `Accept: application/vnd.vera.v0.4+json` 时使用 0.4 语义，成功 JSON 响应带相同 `Content-Type` 和 `Vary: Accept`；缺省或请求 `application/json` 沿用普通 JSON。尚未部署 0.4 的服务应返回 406，客户端还必须核对响应 `Content-Type`，防止旧服务忽略 `Accept` 并返回旧格式。健康检查、认证跳转和 PDF 按原媒体类型处理，错误可用原错误 JSON。新建 0.4 计划使用 `collect_latest`；读取历史任务时可返回 `fixed_legacy`。创建与确认须协商同一版本并使用相应 `Content-Type`。0.4 的 `quote_requirement` 可省略，服务端默认 `latest_available` 并将解析后的值纳入计划哈希。`/me.research_protocol=v0.4` 表示已配置该路径，不证明真实字段与时延已验收。
- 需要幂等的写请求带 `Idempotency-Key`（1–128 字符）。同一 workspace、方法、规范化路由和键：请求哈希相同则返回原响应，不同则返回 409。幂等记录保存 30 天。
- 列表：`page_size` 默认 20、上限 100；不透明游标，按 `created_at` + `id` 稳定排序。
- 双厂商部署的新网页任务使用普通 JSON 创建固定截止计划；仅 Choice 部署继续按 `/me` 协商既有默认协议。网页不传 `quote_requirement`。`/me.realtime_trade_available=true` 表示现有 0.4 API 客户端可显式请求 `realtime_trade`；确认和核验实际成交时仍重新检查。已创建的计划按其原协议确认，历史报告也按其原协议读取。

## 2. 路由

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET | /healthz | 进程存活 |
| GET | /readyz | 检查数据库与已登记的数据源；启用严格行情门禁时还检查全市场 Choice 治理记录。不向厂商或模型发请求。 |
| GET | /auth/login、/auth/callback | 登录与回调；state、nonce、PKCE；防开放重定向；首次登录需有效邀请码 |
| POST | /auth/logout | 销毁会话 |
| GET | /api/v1/me | 用户、workspace、CSRF token、配额剩余 |
| GET | /api/v1/skills | 技能列表、版本、参数 Schema、当前可用性 |
| GET | /api/v1/entities | `q` 搜索；`kind` 过滤 stock/industry/market；返回各技能可用性 |
| POST | /api/v1/research-subjects/{id}/prepare | 双厂商部署为选中的 A 股准备 TuShare 数据。普通 JSON、登录和 CSRF 校验。排队返回 202 及 Retry-After，成功或失败返回 200 及 ready/failed。相同证券的轮询共用在途采集。 |
| POST | /api/v1/research-plans | 一句话或结构化输入生成计划；返回计划或候选列表；不预留配额 |
| GET | /api/v1/research-plans/{id} | 读取未过期计划 |
| POST | /api/v1/research-tasks | 用 `plan_id` + `plan_hash` 确认计划并创建任务，幂等，202 |
| GET | /api/v1/research-tasks | 任务列表，可按技能、状态、对象过滤 |
| GET | /api/v1/research-tasks/{id} | 状态、revision、步骤进度、原因、`report_ref` |
| POST | /api/v1/research-tasks/{id}/cancel | 校验 `expected_revision`；202 取消处理中 / 200 已取消 / 409 已完成；observation.v1/0.4 成功返回 204（无厂商数据，授权失效仍可取消） |
| GET | /api/v1/reports | 报告列表，可按对象、技能过滤 |
| GET | /api/v1/reports/{id}/versions/{version} | 已交付的报告：抬头、章节、结论、表格、图表数据、限制 |
| GET | /api/v1/reports/{id}/versions/{version}/series/{series_id} | 报告图表数据，来自该报告冻结的数据快照，不取实时数据 |
| GET | /api/v1/evidence/{id} | 证据版本：来源、口径、时点、原文片段或数值；不返回可公开访问的永久 URL |
| GET | /api/v1/calculations/{id} | 公式、版本、输入引用、结果 |
| POST | /api/v1/reports/{id}/versions/{version}/feedback | 反馈：useful/confusing/incorrect/other，可关联 claim；备注 ≤ 2000 字；幂等，201 |
| GET / POST | /api/v1/conversations | 历史列表 / 首条消息。列表沿用 `limit`、`cursor`。接受消息返回 202、`Location` 和 `Retry-After`。 |
| GET / DELETE | /api/v1/conversations/{conversation} | 读取最近轮次或用 `before` 加载早期轮次 / 删除对话。 |
| POST | /api/v1/conversations/{conversation}/turns | 发送消息或引用同一会话中的轮次。 |
| GET | /api/v1/conversations/{conversation}/turns/{turn} | 单轮详情，按状态返回候选或待确认计划。 |
| POST | /api/v1/conversations/{conversation}/turns/{turn}/actions | 选择对象、确认计划或取消研究。成功返回 204。 |
| GET | /api/v1/conversations/{conversation}/turns/{turn}/result | 只读取已交付报告的对应版本。短研究返回结论、数据与图表。 |
| GET / POST | /api/v1/watchlist | 列表 / 添加 |
| PATCH / DELETE | /api/v1/watchlist/{item_id} | 修改标签、备注、提醒 / 移除 |

轮询：任务详情响应带 `Retry-After`，执行中建议 3 秒，可退避到 15 秒，终态后停止。首版不提供 SSE。

### 对话协议

对话接口使用普通 JSON，需要登录且已开通 workspace。写请求校验 Origin 和 CSRF。客户端按 `/me.conversation_topics` 展示可用的短研究主题。每次发消息提交 UUID `client_message_id`。在可见历史中重发同一请求返回同一轮次，重用编号发送不同内容返回 409。追问提交会话 `expected_revision`，可带 `reply_to_turn_id`。确认提交轮次 `expected_revision`、`plan_id` 和 `plan_hash`。已开始任务的取消另需要 `task_revision`。选择候选或确认重试均受轮次状态控制。

活动中的会话详情带 `Retry-After`。轮次终态从任务读出。结果与被引用的历史轮次绑定，读取会检查当前的数据用途与快照权限。短报告使用对话历史，完整研报也出现在原报告列表中。对话删除后短结果和关联证据失去该对话的可读引用。仍被另一份可读报告引用的数据按该报告的授权规则管理。

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

修改契约时同步更新 OpenAPI、JSON Schema、Go DTO、前端类型和契约测试。前端类型由 OpenAPI 生成（openapi-typescript 7.13.0，`cd web && npm run gen:api`；`make check-api-types` 检查漂移），不手写重复类型。删除或改名字段需要兼容窗口，`make check-contract-compat` 拦截不兼容变更；确需破坏时在 `contracts/compat-exceptions.json` 登记理由与批准记录（PR/ADR）；已冻结的报告不随 schema 变化而改写。

## 5. 浏览器投影

OpenAPI 中的 Claim、Evidence 是报告页的读取投影，不是模型的输出结构；模型输出结构见 [HARNESS](docs/HARNESS.md) §6.2。浏览器不能回传或修改 Claim。证据的来源等级、口径和原文位置由数据服务写入，展示时不由模型补充。

## 真实成交观察简报协议

`application/vnd.vera.observation.v1+json` 是休市可用的独立简报协议，保留 demo.2 的非实时语义。`/me.research_protocol=observation.v1` 表示当前部署启用该协议。客户端按部署声明选择协议并核对响应媒体类型；旧 fixture 部署使用 application/json。0.4 的实时条件保持不变。

新计划版本 `stock_analysis/1.1.0-demo.2`，参数为空，`snapshot_mode=collect_latest`，`quote_requirement=null`。计划/任务 `as_of` 是预览时间；确认后采集，任务 `snapshot_as_of` 在冻结前为空、冻结后不可变。确认必须使用该协议；旧客户端不得创建或确认新计划。计划、任务与报告新增字段均为兼容的可选字段，旧任务仍用 fixed_legacy。报告 as_of 与 snapshot_as_of 均为实际冻结时间。

报告只提供真实厂商日成交观察和完整来源追溯，标为 limited、非实时；证据的 received_at 是实际采集时间，event_at/published_at 为空表示未确认，不能由序列日期伪造。证据只对已交付且属于当前 workspace 的报告开放，每次读取（含 PDF/series）核验完整快照与原始对象。旧 observation 路径沿用当前凭证授权实现，不等于 demo.3 的 `source_licenses` 逐用途复核；demo.3 在模型发送和展示时分别验证当前模型/展示许可。未接入的财务/实时字段不送模型。

真实成交观察的证券 `status` 可为 `unknown`，表示尚未核实交易状态；不得据此推断停牌或休市。证据 `origin` 使用既有 `vendor_derived`，不将厂商观察冒充交易所官方发布。
