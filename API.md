# 首版 HTTP API

契约源：[contracts/openapi.json](contracts/openapi.json)，OpenAPI 3.1；请求结构以 contracts/schemas/ 中 JSON Schema 2020-12 为准。当前契约是实现目标；健康端点外的业务路由尚未实现，见 TASKS.md。

## 1. 身份与通用行为

- 浏览器同源 API `/api/v1`，OIDC Authorization Code + PKCE 由后端完成，HttpOnly/Secure/SameSite=Lax 会话 cookie；不把访问 token 放 localStorage。
- 开发仅本地可配置不使用 Secure cookie，生产启动拒绝。写请求验证 session 绑定的 CSRF token 与 Origin；token 从 GET /me 获取。
- 服务端从会话确定 workspace，客户端不提交 tenant_id；证据/报告检查父任务归属及当前许可，越权资源统一 404。
- 所有业务 JSON 响应带 request_id（健康探针和登录重定向除外），日志不存原文、密钥和 cookie。JSON 时间 RFC3339；decimal 用字符串；ID 为服务端 UUID，fixture 使用隔离 DEMO 命名空间。
- 需要幂等的写方法带 Idempotency-Key（1–128 字符）。同 workspace+HTTP 方法+规范化路由+键同 hash 返回原响应，不同 hash 返回409；保存30天，核心业务唯一约束永久防重复。
- 列表 page_size 默认20，上限100；不透明游标含排序边界，created_at+id 稳定排序；每页重新授权。响应不承诺跨页冻结。

## 2. 路由

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET | /healthz | 存活，不检查模型；无敏感配置 |
| GET | /readyz | 当前骨架返回503；实现后检查必需启动配置和业务库，不以模型即时健康触发重启 |
| GET | /auth/login、/auth/callback | 登录重定向与回调；state/nonce/PKCE、防开放重定向 |
| POST | /auth/logout | 销毁本地 session，CSRF |
| GET | /api/v1/me | 用户、workspace、CSRF token、配额摘要 |
| GET | /api/v1/entities | q 搜索数据源覆盖主体；支持 kind 筛选 stock/industry/other，按名称/代码返回稳定 ID、类别及能力状态 |
| POST/GET | /api/v1/research-tasks | 幂等创建202 / 分页列表 |
| GET | /api/v1/research-tasks/{id} | 阶段、revision、reason、可选 report_ref；无未验证正文 |
| POST | /api/v1/research-tasks/{id}/cancel | 校验 expected_revision；202取消处理中/200已取消/409已完成 |
| GET | /api/v1/reports/{id}/versions/{version} | 冻结报告和覆盖情况；只读已交付版本 |
| GET | /api/v1/evidence/{id} | 通过 evidence_ref 对应唯一版本 ID 查询原文位置/许可内片段，不返回公开永久URL |
| GET | /api/v1/calculations/{id} | 输入版本、公式与结果 |
| POST | /api/v1/reports/{id}/versions/{version}/feedback | useful/confusing/incorrect/other；备注最多2000字，幂等201 |

轮询使用 Retry-After 建议3秒，可退避到15秒，终态停止；首版无 SSE。重新研究使用新的创建请求和新幂等键，不能自动覆盖旧报告。

## 3. 错误

错误信封 `{request_id, error:{code,message,retryable,details}}`。400 参数/范围错误；401 未登录；403 CSRF/用途拒绝；404 资源不可见；409 幂等冲突/并发版本冲突；422 预检查可确定的范围不支持；429 配额；503 依赖不可用。创建后发生的缺证是任务 ABSTAINED，不把成功查询任务写成 HTTP500。

业务终态 COMPLETED/ABSTAINED/BUDGET_EXHAUSTED/CANCELLED/BLOCKED/FAILED；result_kind 仅在可读报告上是 full/limited。取消、反馈和交付均不能接受客户端声明的验证通过。

## 4. 变更

Schema、OpenAPI、Go DTO、前端类型一起更新，新增契约测试。生成器产物不能替代运行时语义验证；跨期口径、授权、价格和预算由业务服务检查。升级删除字段要经过版本兼容窗口，不默默改变已冻结任务的 schema。

## 5. 领域对象与浏览器投影

OpenAPI 的 Claim 是报告页读取投影，并非模型直接输出结构；模型候选沿用 Agent 研究文档的领域协议，在验证和交付后由服务端转换。浏览器不回传 Claim，也不能改其状态。evidence_ref 指向被冻结的证据版本；GET /evidence/{id} 使用该版本的 UUID，而非厂商记录 ID。原文位置、来源级别和引用许可由数据治理写入，禁止在展示时用模型补造。
