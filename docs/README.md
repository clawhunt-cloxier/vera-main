# 开发资料导航

## 当前规范与历史研究

根目录 SPEC/ARCHITECTURE/API/DATA_MODEL 是实施基线；contracts 是机器可检查的传输契约。AGENTS/CONTRIBUTING 定义协作，TASKS 定义事实进度。若冲突，应修正规范与契约，而不是让代码各选一套。

[首版决定](decisions/0001-p0-baseline.md) 收敛全部Q项；[协作流程](COLLABORATION.md)管理Agent交接；[上线依赖](RELEASE.md)只约束真实开放，不阻塞框架开发。

## 按任务阅读

| 工作 | 必读 |
| --- | --- |
| 所有任务 | 根 AGENTS、SPEC、TASKS 与当前handoff |
| API与身份 | API、contracts/openapi.json、DATA_MODEL、SECURITY |
| 数据治理 | 当前决定 + research/financial-research-data-governance-design.md |
| Agent/Worker | ARCHITECTURE + research/financial-research-multi-agent-design.md |
| 报告/前端 | 当前SPEC + research/financial-research-mvp-report-spec.md、模拟报告 |
| 部署与恢复 | RELEASE、TESTING、research/financial-research-backend-design.md §14–15 |

## 完整研究档案

[research/README.md](research/README.md) 提供九份材料的详细导读，涵盖产品、后端、治理、Agent论文、RPC、年度模板、模拟样例、历史决策和变更。

这些文件完整迁入以保留研究依据，但不再是待填写的当前决策表。模型价格、具体型号与论文状态是原研究快照，上线采购前复核。原“机构内部试用”“待确认”的范围已由根SPEC覆盖为基金经理及散户网站试用。P1/P2能力不因写在研究文档中就成为首版任务。

## 外部工程依据

- [AGENTS.md 开放格式](https://agents.md/)：独立项目指令入口与命令说明。
- [Claude Code 项目记忆](https://code.claude.com/docs/en/memory)：CLAUDE.md 导入，实际客户端确认加载；本仓库不依赖私有会话记忆。
- [GitHub Spec Kit](https://github.com/github/spec-kit)：借鉴规格、计划、任务可追溯；不安装另一套自动审批流程。
- [Go 发布策略](https://go.dev/doc/devel/release)、[Node 发布周期](https://nodejs.org/en/about/previous-releases)：工具链冻结和维护期复核。
