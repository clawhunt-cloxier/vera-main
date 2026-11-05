# Vera

Vera 是面向一线基金经理与散户投资者的金融研究 Agent 试用网站。首版帮助用户对一家 A 股制造业公司的一份年度报告进行有证据的点评，展示研究进度、结论、来源与计算过程。

**当前状态：协作仓库、冻结的首版规范，以及可运行的 Web/API 工程起点。** 尚未实现真实研究、登录或数据厂商接入；页面会明确显示未实现状态，不以模拟报告冒充真实研究。

## 开始开发

先读 [AGENTS.md](AGENTS.md)、[SPEC.md](SPEC.md)、[TASKS.md](TASKS.md)。Claude Code 通过 [CLAUDE.md](CLAUDE.md) 导入同一协作规范。详细资料入口是 [docs/README.md](docs/README.md)。

- Go 1.27.3；Node 26.13.0；Python 3.11+（文档检查）。
- `make setup`：创建文档检查虚拟环境并安装锁定的前端依赖。
- `make verify`：检查文档/契约、Go 测试与前端构建；不证明研究业务已实现。
- `make dev-api`：本地 API，默认 127.0.0.1:8080。
- `make dev-web`：前端开发服务器，默认 127.0.0.1:5173；先在 web 中 `npm ci`。
- `GET /healthz`：进程存活；业务接口按 [API.md](API.md) 逐步实现。

仅重装前端依赖可用 `make setup-web`。生产构建和部署将在 T009 完成，当前没有自动生产部署。

## 文档地图

| 文件 | 用途 |
| --- | --- |
| [SPEC.md](SPEC.md) | 首版目标、范围、用户流程、验收与性能目标 |
| [ARCHITECTURE.md](ARCHITECTURE.md) | 实际计划实现的模块、进程、数据流与状态所有权 |
| [API.md](API.md) | HTTP、认证、幂等、错误和分页约定 |
| [DATA_MODEL.md](DATA_MODEL.md) | 表、版本、租户隔离、事务与恢复不变量 |
| [TASKS.md](TASKS.md) | 可领取的任务、依赖、状态与验收命令 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 分支、worktree、PR、审查和交接流程 |
| [TESTING.md](TESTING.md) | 当前检查与未来业务验收的区别 |
| [docs/decisions/0001-p0-baseline.md](docs/decisions/0001-p0-baseline.md) | Q-01–Q-24 的首版决定、参数与外部依赖 |
| [docs/research/README.md](docs/research/README.md) | 完整历史研究、论文、模型实验与后续扩展设计 |

首版优先完成框架与基本体验；合规适用性核查放在开发后期。在线研究始终自动执行，不设置人员审核状态。未采购的数据许可、模型密钥与域名不能由 Agent 假定已经具备，见 [上线依赖](docs/RELEASE.md)。

开发从 origin/dev 开始，main 为稳定发布线，见 [分支规范](docs/BRANCHING.md)。
