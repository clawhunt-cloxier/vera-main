# CI Action 运行时升级

任务：CI 运行时警告；owner：Codex；分支：agent/codex/ci-action-runtime；基线：dev 57804fe。

用户观察到 dev push Actions 警告，并确认日志似乎指向 Node 20 运行时。GitHub 连接器与未认证 Actions API 返回404，尚不能独立读取运行日志。

逐个读取固定提交的 action.yml，确认 checkout v4、setup-go v5、setup-node v4、setup-python v5 都声明 node20。根据官方迁移公告，升级至 checkout v5、setup-go v6、setup-node v6、setup-python v6；从官方 tag 解析完整 SHA 并验证新 action.yml 均声明 node24。项目 Node 26.13.0、Go go.mod、Python 3.12、权限、缓存、触发条件与测试步骤均保持不变。

依据：[GitHub Node 20 移除公告](https://github.blog/changelog/2026-09-23-node-20-is-no-longer-available-in-github-actions/)。GitHub 托管 ubuntu-24.04 runner 执行；若以后使用自托管 runner，需满足新版 Action 所需 runner 版本（至少2.327.1）。

验证：工作流 YAML 解析、除 uses 引用外配置等价、新旧 Action 声明比对、文档链接及 git diff --check。本地没有模拟 GitHub 托管 runner，未声称远端警告已消除；需在 PR 和 dev 合并后观察实际运行。

状态 REVIEW；PR 目标 dev。未部署业务，未修改分支保护，未隐藏或屏蔽警告。修复只针对已确认的旧运行时配置，不排除日志还含其他警告。

## 同分支追加：dev 合并后清理任务分支

用户要求将自动清理加入本次 CI 修复分支。新增 closed/merged 事件工作流，限本仓库与 dev 目标，保留长期分支、hotfix、打开 PR 依赖和新增提交。使用精确 SHA 的 Git 删除 lease；仅维护作业有写权限，不检出来源代码。新增离线保护条件测试并接入 make verify，更新分支与本地清理说明。

验证记录：离线清理保护测试、文档检查、工作流 YAML 解析；另使用临时本地 bare Git 仓库验证旧 SHA 删除请求被拒绝，匹配 SHA 才删除。未删除任何真实远端分支；GitHub 实际触发/权限待本 PR 合并后观察。
