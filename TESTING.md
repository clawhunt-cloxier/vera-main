# 验证与验收

ADR-0004 的 [盘中时点与来源样例](tests/fixtures/intraday-cases.json) 是 v1.1 目标规格样例，目前尚未接入可执行评测；不得以样例存在宣称实时研究通过。实施验收须覆盖过期/延迟、权限缺失、盘后、未来披露、归档提交边界和冻结后的重试恢复。

## 1. 当前可运行

- `make check-docs`：相对链接、显式锚点、JSON 代码块、任务 ID 唯一、关键文件、模拟算术夹具。
- `make check-contracts`：OpenAPI 3.1 校验，计划、任务、策略 Schema 的示例与非法输入用例。
- `make check-contract-compat`：把 `contracts/openapi.json` 与 `contracts/schemas/` 同基线版本比较（`CONTRACT_BASE_REF`，默认与 origin/dev 的 merge-base；CI 的 PR 用目标分支 SHA，推送用推送前 SHA）。删除操作、成功响应或响应字段，响应字段不再必需，请求新增必填项，请求类型、枚举、长度收紧，均判为不兼容；响应枚举新增只告警。确需破坏时在 `contracts/compat-exceptions.json` 逐条登记 finding、reason、approved_in。
- `make check-api-types`：用锁定版本的 openapi-typescript 重新生成前端类型并与 `web/src/api/schema.d.ts` 比较；不一致时执行 `cd web && npm run gen:api`。
- `make check-toolchain`：Go ≥ 1.27、Node ≥ 26 的版本检查。
- `make test-go`：健康与就绪接口的 Go 测试。
- `make test-web-client`：使用模拟 HTTP 响应验证客户端协议协商、0.4 降级拒绝、旧计划确认、历史报告读取和取消；已纳入 `make verify` 与 CI，无浏览器或厂商依赖。
- `make build-web`：TypeScript 检查与 Vite 生产构建。
- `make test-automation`：用离线 API 替身验证“合并后自动清理任务分支”的保护条件；不请求 GitHub，也不删除真实分支。真实触发与权限需在合并后查看维护作业。
- `make verify`：以上全部。CI 不使用真实密钥，不调用收费模型。

Verify 在 dev/main 的推送和以 dev/main 为目标的 PR 上运行。Branch policy 只检查 PR 来源分支，不证明业务正确。

前端页面验收可运行 `cd web && npm ci && npx playwright install --with-deps chromium && npm run test:realtime-ui`。该命令需要下载 Playwright Chromium 及系统依赖；使用标注为模拟的 API 响应，在桌面和手机视口检查 0.4 选择、确认、冻结时间、证据展示，并回归旧 observation 计划、历史报告与取消流程，不调用真实数据厂商或模型。浏览器用例不替代真实行情时点与授权的端到端验收。

### 真实部署的盘中浏览器验收（显式运行）

`npm run accept:live-realtime` 通过部署网站选择证券，确认页面不再提供“行情要求”控件，然后用同源 v0.4 API 显式创建 `realtime_trade` 计划，再回到网页检查计划、提交一次任务、等待交付、打开报告和证据侧栏。严格模式的 API 请求只用于保留兼容的盘中验收路径，网页默认流程始终省略 `quote_requirement`。脚本只使用当前登录会话，不拦截或改写网页请求；会消耗真实厂商和模型额度，并保留一项真实研究任务。它不在默认测试、`make verify` 或 CI 中运行，CI 环境显式拒绝。脚本没有实现真实厂商语义证明，成功仍依赖已核实的 TIME、交易状态、延迟及用途许可登记；非交易时段、停牌、弃权、权限不足或协议降级均为验收失败。

初始 MVP 的真实验收运行 `npm run accept:live-latest`（同样支持 `-- --check-config`）。它检查网页没有“行情要求”选择控件，默认请求省略 `quote_requirement`，服务端计划和任务持久化为 `latest_available`，并校验 `TIME,NOW` 策略下报告和证据标出的未知延迟与交易状态、真实来源、冻结年龄、时间顺序和归档哈希。该模式不证明实时成交；需要盘中策略时再单独运行严格实时验收。

运维先用已有测试账户登录目标站点，在仓库外的受控目录保存 Playwright `storageState`；文件须由当前用户持有且权限为 `0600`，不要提交文件、cookie、账号口令、CSRF 或授权材料。脚本不创建账户、不读取账号口令、不保存或更新登录状态，不自动重新提交失败或超时的任务。超时后应在该测试账户的正常任务页核对在途任务，避免直接重跑产生第二项研究。

运行前安装锁定依赖与 Chromium（`cd web && npm ci && npx playwright install --with-deps chromium`）。在操作机设置 `VERA_LIVE_BROWSER=1`、目标 HTTPS 站点 `VERA_LIVE_BASE_URL`、会话文件 `VERA_LIVE_STORAGE_STATE`、本次验收证券 `VERA_LIVE_STOCK_CODE`、实际来源 `VERA_LIVE_PROVIDER_ID` 与 `VERA_LIVE_PROVIDER_NAME`。`VERA_LIVE_MAX_AGE_MS` 须取自已登记的策略，用于核对**冻结时**的行情年龄；`VERA_LIVE_TASK_TIMEOUT_MS` 可选。操作机时钟须与服务端同步，脚本容许 5 秒差异。

`--check-config` 只离线校验配置及登录状态文件，返回 `configuration_valid` 不代表实时验收通过。缺配置、会话失效、运行错误或断言失败都返回非零退出码，不跳过或输出假成功。运行时关闭 `DEBUG`、`PWDEBUG`、`NODE_DEBUG`；脚本不输出异常原文、响应正文、报价、报告文本、用户/workspace/任务 ID，也不保存截图、trace、HAR 或视频。仅输出 JSON 验收元数据：结果/阶段/错误码、技能版本、来源校验结果、真实标记、来源与冻结时点、新鲜度、结论/证据数量和耗时。

验收同时检查 `Me.synthetic=false`、报告 `synthetic=false`、计划/任务的行情策略（网页默认 `latest_available`，严格模式 `realtime_trade`）、0.4 媒体类型、不可混淆的预览与冻结时点、对应的新鲜度和交易状态、报告与证据的事件/接收/可用时间完全一致及先后关系、指定厂商 ID/显示名称、归档哈希定位，以及网页中秒级时间与来源展示。当前 Evidence 没有独立 `synthetic` 或结构化交易状态字段：脚本从同一报告引用取得证据，核对来源与交易状态说明，不能把 `fresh` 当成交易状态。

## 2. 按任务补齐的测试

每个任务在实现时加入真实的测试命令；任务未完成时不提供永远成功的占位脚本。

| 任务 | 必须有的测试 |
| --- | --- |
| T011 | golangci-lint（`make lint-go`，已实现）、前端 lint 与格式检查（`make lint-web`，已实现）、OpenAPI → TS 类型生成一致性检查（`make check-api-types`，已实现）、契约兼容性检查（`make check-contract-compat`，已实现，`scripts/test_contract_compat.py` 覆盖判定规则）、工具链版本检查（`make check-toolchain`，已实现） |
| T012 | 迁移在真实 PostgreSQL 上执行；RLS 与复合外键；唯一约束并发测试；River 事务入队 |
| T013 | OIDC 登录回调（state/nonce/PKCE）、CSRF、会话过期、邀请码、跨账号访问返回 404 |
| T014 | 每个厂商映射的样本测试；单位与口径转换；对账冲突；新鲜度；fixture 在 production 模式拒绝启动 |
| T015 | 实体搜索（代码、简称、拼音、曾用名）、能力登记与章节可用性 |
| T016 | 公式库：每个公式的适用条件、零值、负值、停牌、缺失；Decimal 精度与舍入 |
| T017 | 模型网关：录制回放、超时、429、空 content、截断、费用核销与 UNKNOWN 对账 |
| T018 | Harness：规则 R-01–R-09 单元测试；步骤幂等；Worker 重启；取消与交付竞争；截止时限 |
| T019 | 三项技能用 fixture 端到端生成 full、limited、ABSTAINED 报告；注入与禁用表达对抗样例 |
| T020 | 评测执行器与评测集，见 §3 |
| T022 | Playwright：登录 → 一句话 → 确认计划 → 进度 → 报告 → 证据侧栏 → 反馈 → 自选股；手机视口 |
| T023 | 部署冒烟、备份恢复演练、压测 |

`tests/fixtures/annual-review.json` 是财务公式的模拟回归样本（F01–F08），`check-docs` 会核对其算术。

## 3. 质量评测

评测集（EXT-04，需要真实授权数据）：

- 个股分析 20 只、资金动向 15 个对象（个股 8、行业 5、全市场 2）、深度研究 10 个（公司 5、行业 3、公司+行业 2）。
- 覆盖至少 5 个申万一级行业，包含亏损公司、ST 公司、近期上市公司、金融类公司和停牌股票。
- 对抗样例：在公告或新闻中注入指令、诱导评级与目标价、单位错位、厂商口径冒充官方数据、数据修订、成分调整。

指标与门槛：

| 指标 | 定义 | PILOT 门槛 |
| --- | --- | --- |
| 数字溯源通过率 | 已交付报告中数字匹配事实的比例（确定性检查）| 100%，任何例外都是缺陷 |
| 禁用表达放行数 | 已交付报告中出现评级、目标价、操作建议、概率的次数 | 0 |
| 严重错误数 | 人工核查发现的事实错误、方向错误、口径混用、时间泄漏 | 0 |
| 推断支持率 | 人工核查认为“有依据”的 inference/scenario 结论比例 | ≥ 90% |
| 正常样本交付率 | 数据齐全的样本交付 full 或 limited 的比例 | ≥ 90% |
| 结论存活率 | 写作产出的结论通过核验的比例 | 记录，不设门槛；用于调提示词 |
| 成本与时延 | 每技能的费用、模型调用次数、P50/P95 时延 | 在 ADR-0003 预算内 |

人工核查由用户指定的投研人员按核查表进行，不由同一模型自评。评测结果连同模型版本、技能版本、提示词版本保存在 `docs/evals/`。

模型对照：同一冻结快照分别用 `deepseek-flash`（思考关闭）、`deepseek-flash`（思考开启）、`deepseek-v4-pro` 运行，比较以上指标，结果决定 writer 与 verifier profile 的默认配置。

## 4. 容量与恢复

- 压测：20 个活跃用户，30 个在途任务，模型与厂商用延迟可配置的模拟服务；真实环境另行测量。
- 恢复：Worker 进程被杀、数据库短暂不可用、模型 429 与响应丢失、厂商超时；验证不重复交付、费用正确记账、任务最终进入终态。
