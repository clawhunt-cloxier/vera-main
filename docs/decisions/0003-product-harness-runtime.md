# ADR-0003：首版产品模块、投研 Harness 与运行时

状态：已采纳，待所有者审阅。日期：2026-11-06。决定依据：仓库所有者确认了 D1、D2、D4、D5 的产品范围，并授权助手决定其余技术细节；本文由 Claude Code 起草。

取代范围：ADR-0001 中 Q-01、Q-03、Q-09、Q-10、Q-12、Q-13（开发身份源）、Q-17–Q-22 与本文冲突的部分；ADR-0002 的 `general_research` 模板、`stock/industry/other` 对象分类和 `research_question + time_range` 创建请求。ADR-0001 的其余决定继续有效，例如私人 workspace、全自动无人工审核、来源默认禁用、无 Redis、留存期限。

## 背景

用户确认的最终目标：

1. 重构 [finance-suite](https://github.com/jinny280401-glitch/finance-suite) 与 [Linmeimei-Agent](https://github.com/jinny280401-glitch/Linmeimei-Agent)，首版做出接近原产品的 demo，供一线投研人员试用。
2. 网页应用：登录后在私人 workspace 中使用研报生成、个股分析、资金动向和自选股。后端把用户需求交给 Harness 完成研究，按标准研报格式交付成品。
3. 结论可信：只使用我们接入的可信数据源，由研究状态机推进；多 Agent 研讨后置为可选。
4. 原系统以 OpenClaw 加载长提示词（如 317 行的 stock-analyst.md），由模型一次写完整份报告；取数靠 AkShare/东财抓取和 Tavily/Brave 联网搜索。新系统自建 Harness，目标是用 Flash 级模型（例：DeepSeek-V4.1-Flash，API 调用）完成投研，同时提高性能、降低模型要求。

同类产品参考：阿里云 2026 年发布的 AI 投研工作台 [Qovest](https://www.qovest.cn)。公开报道称其采用自研的金融场景 Harness Agent 框架和采购的权威金融数据库，支持一句话提交需求、自动取数分析生成研报、按设定频率持续跟踪，并提供产业链图谱与动态看板（[AIbase 报道](https://news.aibase.com/news/32069)）。其内部实现未公开，本文只借鉴产品形态。

## 决定

### D1 首版模块

| 技能 ID | 名称 | 研究对象 | 原项目对应 | 模型参与 |
| --- | --- | --- | --- | --- |
| `deep_research` | 研报生成（深度研究） | A 股公司、申万行业、公司+所属行业 | deep-research.md、industry-report.md | 章节写作、语义核验 |
| `stock_analysis` | 个股分析 | A 股个股 | stock-analyst.md | 章节写作、语义核验 |
| `capital_flow` | 资金动向 | A 股个股、申万行业、A 股全市场 | stock-analyst.md §4、market_pulse 的资金部分 | 章节写作、语义核验 |
| `watchlist` | 自选股 | A 股个股 | stock-watcher.md | 不调用模型 |

后置：宏观、集合竞价、视频拆解、咨询报告、投资大师视角、券商业务工具箱、微信入口、报告内追问、定时模型跟踪、增量研究、港美股，以及基金/指数/债券标的。旧年度财报点评模板不再是独立入口，其 F01–F08 公式并入共享公式库，供财务章节使用。

### D2 输出边界

允许五类结论：事实、确定性计算、有依据的推断、归属陈述（“管理层称”“某机构观点”，注明来源）、条件化情景（触发条件 + 观察指标 + 失效条件）。

禁止以下输出：

- 投资评级，以及买入、持有、卖出、加减仓等操作建议。
- 目标价、价格区间、情景概率、仓位或凯利公式。
- 以“低估”“高估”“值得买入”作结论。
- 投资大师人设打分。
- 没有确定性定义的资金标签，如“主力撤离”“游资博弈”。

估值章节只陈述估值倍数、自身历史分位和可比公司分布。

### D3 Harness

自建 Go Harness，由技能（Skill）定义驱动确定性流水线。代码负责取数、计算、选证、组装和裁决。模型只在三处使用：章节写作、逐条语义核验、规则无法解析时的一句话需求解析。P0 模型不调用工具，不决定取数，不做算术。完整设计见 [HARNESS](../HARNESS.md)。

### D4 数据服务

研究只使用登记的数据源，禁止自由联网搜索。新增数据服务模块，聚合多家厂商，统一数据集、字段、单位、时点与证据版本。厂商资料由用户后续提供，接入前用 fixture 适配器开发。设计见 [DATA_SERVICE](../DATA_SERVICE.md)。

### D5 模型

保留单个 OpenAI-compatible 网关，按用途配置 profile（writer、verifier、router）。默认目标模型为 DeepSeek `deepseek-flash`。

2026-11-06 查阅的 [DeepSeek 文档](https://api-docs.deepseek.com/quick_start/pricing) 显示：

- 该别名当前服务于 DeepSeek-V4.1-Flash-1017，上下文 1M。
- 支持 JSON Output 与 Tool Calls；Tool Calls 的 strict 模式为 Beta，需使用 `/beta` 地址。
- 默认开启思考模式。
- 每百万 token 价格（高峰/非高峰）：缓存命中输入 0.006/0.003 美元，缓存未命中输入 0.30/0.15，输出 1.20/0.60。
- 高峰时段为周一至周五 01:00–04:00 和 06:00–10:00 UTC。

精确 model_id、价格表与时段由配置提供，不写入代码默认值。网关记录响应中的实际模型版本，别名指向变化时触发评测告警。写作步骤默认关闭思考模式，是否开启由 T020 评测决定。

### D6 运行时：River 取代 Temporal

任务调度使用 [River](https://github.com/riverqueue/river)。River 是基于 PostgreSQL 的任务队列，MPL-2.0 许可，2026-11-05 发布 v0.51.1，支持 Go 1.26/1.27。任务状态机与步骤台账存在业务库，River 只负责可靠投递、重试、取消和定时任务。

理由：首版规模是 20 个活跃用户、单区域单机。Temporal 需要额外的服务、数据库、Worker 版本管理和确定性约束，开发与运维成本高于收益。River 支持事务内入队，可以替代自建 Outbox。

代价是失去 Temporal 的事件历史回放与可视化，由步骤台账、运行追踪和录制回放测试弥补（见 HARNESS §11）。若后续出现跨服务长事务或多区域需求，再基于步骤台账迁移。

### D7 状态、预算与配额

任务状态改为 QUEUED → COLLECTING → ANALYZING → ASSEMBLING → DELIVERING → COMPLETED，另有 CANCEL_REQUESTED，以及终态 ABSTAINED、BUDGET_EXHAUSTED、CANCELLED、BLOCKED、FAILED。章节级进度通过步骤列表展示。

预算按技能配置，取代 Q-19 的单一上限。以下为初始值，T020 实测后调整：

| 技能 | 截止时限 | 模型调用上限 | 输入 token | 输出 token（含思考） | 费用上限 |
| --- | --- | --- | --- | --- | --- |
| stock_analysis | 6 分钟 | 24 | 240,000 | 36,000 | 0.20 美元 |
| capital_flow | 5 分钟 | 16 | 150,000 | 24,000 | 0.12 美元 |
| deep_research | 15 分钟 | 48 | 600,000 | 80,000 | 0.50 美元 |
| 需求解析（每次） | 20 秒 | 2 | 6,000 | 600 | 0.01 美元 |

按高峰价、零缓存命中估算，三项技能用满 token 时的费用约为 0.12、0.07、0.28 美元，均低于费用上限。

配额取代 Q-20：

- 每用户：同时执行 2 个、排队 3 个、每日 20 个任务，其中 deep_research 每日 5 个。
- 平台：在途任务 30 个，模型并发 32。
- 每日模型费用总上限默认 30 美元，为配置项；达到后拒绝新任务。

目标时延取代 Q-09（已准入任务 P95，不含厂商故障）：个股分析 3 分钟，资金动向 2 分钟，深度研究 10 分钟。

### D8 入口：一句话需求 + 计划确认

用户可输入一句话或选择技能卡片。系统先生成研究计划，列出技能、对象、参数、章节可用性和配额消耗；用户确认后才创建任务。计划在服务端保存 30 分钟，任务严格按确认的计划执行。见 [研究入口](../RESEARCH_SCOPE.md)。

### D9 发布阶段

DEV 使用 fixture 数据，可用录制的模型输出。DEMO 使用真实厂商数据与真实模型，邀请少量内部用户。PILOT 在通过评测门槛后邀请一线投研人员。见 [RELEASE](../RELEASE.md)。

### D10 身份

保留后端 OIDC 客户端、服务器会话与邀请码。开发环境身份源由 Keycloak 改为更轻的 Dex 静态用户，生产 issuer 为部署配置。仍禁止自建密码存储。

## 影响

尚无业务代码与生产数据，不需要数据迁移。创建任务的契约不兼容地改为“先建计划再确认”，OpenAPI、Schema、示例与检查脚本同步更新。原 T002–T009 尚未领取，作废并由 T011–T023 取代，见 TASKS。研究档案中的 Temporal、多角色研讨、gRPC 等内容继续只作背景。

## 风险

- Flash 模型质量：靠窄任务、预计算、证据引用和验证器控制。T020 用同一冻结数据对比 `deepseek-v4-pro` 以量化差距。若某类章节达不到门槛，可只为该章节配置更强的 profile，但不作为默认。
- 厂商未定：章节可用性由能力登记决定。缺可选数据时交付受限报告，不伪造。
- 北向资金：2024-05-13 起停止实时披露，2024-08-19 起个股持股改为按季披露（[港交所通知](https://www.hkex.com.hk/-/media/HKEX-Market/Services/Circulars-and-Notices/Participant-and-Members-Circulars/HKSCC/2024/ce_HKSCC_NOM_217_2024.pdf)）。系统不得把厂商估算展示为“北向资金当日净流入”的官方事实，见 [资金动向](../skills/capital-flow.md)。
- 合规适用性仍在开发后期核查（EXT-05），输出边界本身不构成合规结论。
