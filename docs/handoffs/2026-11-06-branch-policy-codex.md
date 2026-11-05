# 分支规范初始化交接

操作者 Codex；任务分支 agent/codex/branch-policy；基线 main 68b16df。用户要求落实已讨论的分支方案，当前为一次性 dev 初始化，后续按 PR 流程集成。

完成：新增 BRANCHING；同步 AGENTS、CONTRIBUTING、协作/发布/测试/任务说明、README 和 PR 模板；Verify 覆盖 dev/main，新增 Branch policy 来源检查。dev 初始包含规范，不代表通过业务发布验收。

本地验证：check_docs 通过；两个工作流 YAML 解析通过；分支来源脚本10个正反例通过；git diff --check 通过。未修改业务代码，不重复 Go/前端构建；未声称远端 CI 已成功。

远端管理限制：GitHub 连接器请求失败，本机无 gh/API token；SSH 仅支持 Git 读写，不能设置分支保护或创建 PR。BRANCHING 提供远端规则配置项，尚未启用/核验。没有伪造独立审批，没有自动部署。

待办：管理员配置 dev/main 保护；既有 agent/codex/p0-research-scope 的 PR 应以 dev 为目标并完成契约审查（尚未创建 PR），合入前同步 dev 的新规范。日常新任务均从 origin/dev 建分支。main 保持原发布基线。
