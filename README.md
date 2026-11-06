# Vera

Vera 是可信金融投研 Agent 的网页应用。用户登录后在私人 workspace 中用一句话或技能卡片提出研究需求；系统只使用已接入的授权数据，由自建的投研 Harness 自动完成研究，交付每个数字可溯源的标准研报。

首版模块：研报生成（深度研究）、个股分析、资金动向、自选股，面向 A 股。它重构了 [finance-suite](https://github.com/jinny280401-glitch/finance-suite) 与 [Linmeimei-Agent](https://github.com/jinny280401-glitch/Linmeimei-Agent)，目标是用 Flash 级模型（默认 DeepSeek `deepseek-flash`）完成投研。

**当前状态：规范已定，尚未开始业务开发。** 仓库里只有健康检查 API 和一个起始页；登录、数据接入和研究功能都未实现，页面会如实显示。

## 开始开发

先读 [AGENTS.md](AGENTS.md)、[SPEC.md](SPEC.md)、[TASKS.md](TASKS.md)。Claude Code 通过 [CLAUDE.md](CLAUDE.md) 导入同一份协作规范。详细资料见 [docs/README.md](docs/README.md)。

- 工具链：Go 1.27.3、Node 26.13.0、Python 3.12（仅文档与契约检查）。
- `make setup`：创建文档检查用的虚拟环境，安装锁定的前端依赖。
- `make verify`：文档、契约、Go 测试与前端构建检查；不代表研究业务已实现。
- `make dev-api`：本地 API，默认 127.0.0.1:8080。
- `make dev-web`：前端开发服务器，默认 127.0.0.1:5173。

开发从 origin/dev 开始，main 是稳定发布线，见 [分支规范](docs/BRANCHING.md)。

## 文档地图

| 文件 | 用途 |
| --- | --- |
| [SPEC.md](SPEC.md) | 产品目标、首版模块、用户流程、可信性要求、完成条件 |
| [ARCHITECTURE.md](ARCHITECTURE.md) | 技术栈、进程、模块、状态所有权 |
| [docs/HARNESS.md](docs/HARNESS.md) | 投研 Harness：计划、事实表、写作、核验、状态机、模型网关 |
| [docs/DATA_SERVICE.md](docs/DATA_SERVICE.md) | 多厂商数据接入、统一数据集与字段、来源等级 |
| [docs/skills/](docs/skills/) | 四个模块的章节、数据需求、公式与写作约束 |
| [docs/WEB.md](docs/WEB.md) | 网站页面与交互（MVP）|
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | 自建组件、环境、管理 CLI、备份与发布 |
| [docs/CONVENTIONS.md](docs/CONVENTIONS.md) | 目录结构与编码约定 |
| [contracts/](contracts/) | OpenAPI、请求 Schema、模型输出 Schema、数据集字段字典 |
| [API.md](API.md) | HTTP 约定与路由 |
| [DATA_MODEL.md](DATA_MODEL.md) | 表、隔离、事务与竞争 |
| [TASKS.md](TASKS.md) | 任务、依赖、状态 |
| [TESTING.md](TESTING.md) | 检查命令、按任务的测试、质量评测门槛 |
| [docs/RELEASE.md](docs/RELEASE.md) | 发布阶段与外部依赖 |
| [docs/decisions/](docs/decisions/) | ADR；当前有效的总体决定是 ADR-0003 |

研究全程自动执行，不设人工审核。数据厂商、模型密钥、域名等外部条件不能由 Agent 假定已经具备，见 [RELEASE](docs/RELEASE.md)。
