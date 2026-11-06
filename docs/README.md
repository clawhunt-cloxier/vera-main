# 开发资料导航

## 1. 当前规范的优先级

1. [ADR-0003](decisions/0003-product-harness-runtime.md)：当前有效的总体决定。它部分取代 ADR-0001，完全取代 ADR-0002 的模板与请求部分。
2. 根目录 SPEC、ARCHITECTURE、API、DATA_MODEL，以及 [HARNESS](HARNESS.md)、[DATA_SERVICE](DATA_SERVICE.md)、[RESEARCH_SCOPE](RESEARCH_SCOPE.md)、[skills/](skills/)、[WEB](WEB.md)、[OPERATIONS](OPERATIONS.md)、[CONVENTIONS](CONVENTIONS.md)：实施基线。
3. contracts/：机器可检查的契约——openapi.json 与 schemas/（HTTP）、model/（模型输出）、datasets/catalog.json（数据集字段）。与文字规范冲突时以契约为准，并修正文字规范。
4. AGENTS、CONTRIBUTING、[BRANCHING](BRANCHING.md)、[COLLABORATION](COLLABORATION.md)：协作规则。TASKS 记录事实进度。
5. [research/](research/README.md)：历史研究档案，只作背景。其中与上述规范冲突的内容一律不适用。

发现冲突时，修正规范和契约，不要让代码各选一套。

## 2. 按任务阅读

| 工作 | 必读 |
| --- | --- |
| 所有任务 | AGENTS、SPEC、TASKS、CONVENTIONS、最新 handoff |
| 工程门禁（T011） | CONTRIBUTING、BRANCHING、TESTING、API §4 |
| 数据库与身份（T012、T013） | ARCHITECTURE、DATA_MODEL、API、SECURITY、OPERATIONS |
| 数据服务（T014、T015） | DATA_SERVICE、contracts/datasets/catalog.json、tests/fixtures/demo-universe.json、DATA_MODEL §1.2 |
| 公式与模型网关（T016、T017） | HARNESS §5、§9；skills/ 中的公式表 |
| Harness 与技能（T018、T019a/b） | HARNESS 全文、contracts/model/、skills/、RESEARCH_SCOPE、demo-universe |
| 评测（T020） | TESTING §3、HARNESS §11 |
| 网站（T022） | WEB、API、contracts/openapi.json、skills/ 的章节表 |
| 部署与管理（T023、T024） | OPERATIONS、RELEASE、TESTING §4 |

## 3. 历史研究档案的可复用内容

research/ 中以下设计在当前规范中被引用或简化后沿用，可作为实现细节参考：

- 数据治理：统一记录信封、L1–L5 来源等级、冲突与对账、快照冻结（已收敛到 DATA_SERVICE）。
- Agent 设计：Operation 台账、未知外部调用的费用处理、崩溃窗口分析（已收敛到 HARNESS §9–10 与 DATA_MODEL §4）。
- 年度模板：F01–F08 公式与财务口径规则（并入 skills/stock-analysis.md §4）。

不适用的部分：Temporal 工作流与队列设计、多角色研讨与投票、T2 路由、gRPC 与 mTLS、Redis 缓存层、向量检索与重排、K8s 与多可用区部署。

## 4. 外部参考

- [AGENTS.md 开放格式](https://agents.md/)、[Claude Code 项目记忆](https://code.claude.com/docs/en/memory)：协作入口文件。
- [River](https://github.com/riverqueue/river)：PostgreSQL 作业队列。
- [DeepSeek API 文档](https://api-docs.deepseek.com/)：模型、价格、JSON Output、Tool Calls、上下文缓存。
- [Qovest](https://news.aibase.com/news/32069)：同类产品的公开介绍。
- [FinFIRST](https://arxiv.org/abs/2609.25192)：金融信息检索、来源核验与可追溯性评测，评分分为取数、来源核验、计算与答案三个维度。
