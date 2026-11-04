# 初始化交接：T000 / T001

- 操作者：Codex；分支：main；用户明确授权空仓库初始化并直接 push。
- 时间：2026-11-04 20:26 UTC（本地 2026-11-05）。
- 基线：远端空仓库，无父提交；本交接属于初始化提交，确切 SHA 由 `git log` 获取，避免自引用哈希。
- Git 提交使用仓库所有者的 GitHub noreply 身份；实际文件由 Codex 生成和校验，并非声称所有者手写或 Claude 已审查。

## 已完成

1. 冻结 P0：基金经理与散户邀请试用网站，A 股制造业单公司年度点评，Go 主导；合规适用性开发后期核查；无逐报告人工审核。
2. 创建根规范、ADR、机器契约、模拟算术夹具、协作/任务/发布/测试说明，完整历史资料归档到 docs/research。旧机构内部用途被 trial_research 替代。
3. Go API 存活/就绪探针、超时与信号关闭，React 起始页，锁定依赖、Makefile、GitHub Actions CI。
4. 本地运行文档/链接/JSON、OpenAPI/Schema、非法输入、Go race、TypeScript/Vite、go vet 检查。26 个 Mermaid 块语法解析通过。API 实际启动后 healthz=200、readyz=503，SIGTERM 退出码0。

## 验证复现

安装 Go 1.27.3、Node 26.13.0、Python 3.11+ 后：

```sh
make setup
make verify
cd backend
go vet ./...
```

本次机器未预装 Go，测试使用官方下载且核对 SHA256 的 /tmp 工具链，不提交工具链文件。文档工具实际运行 Python 3.14，CI 固定 Python 3.12；CI 尚须以远端结果为准。Mermaid 使用仓库外已有解析器做一次性语法验证，未做截图或浏览器视觉验收，不是当前 make verify 的组成部分。npm 安装审计返回0项漏洞，这是安装时结果，不是持续安全保证。

## 未完成与限制

- T002–T009 尚未实现。当前没有登录、数据库迁移、真实 Provider、模型调用、Temporal Workflow、报告或完整浏览器 E2E。
- 健康探针仅说明进程运行；readyz 有意返回503，不能当作可上线试用版本。
- 契约是后续实现目标，业务路由当前404，不是成功 stub。模拟算术检查不是研究业务测试。
- 尚无独立 Claude/维护者工程审查；用户已授权本次直接初始化。未来变更走 CONTRIBUTING。
- 没有部署线上服务；真实数据产品、API密钥/价格、部署域名和金标准按 EXT-01–05 准备。它们不阻塞无密钥开发。

## 下一位 Agent

从远端 main 建立独立 worktree，读取 AGENTS/SPEC/TASKS 与本记录，领取 T002。先完成隔离开发 Compose、业务 PostgreSQL/Temporal 库、对象存储、迁移和租户不变量测试。不要先拆微服务或引入完整多 Agent 投票框架。

T002 稳定后，T003 身份与 T004 治理可按已冻结契约分支开发。前端可同时准备页面组件，但完整体验验收等待 T006。实际接入厂商/模型后才确认数据覆盖、成本和延迟，不能用研究文档中的型号或旧价格直接配置生产。
