# 研究范围调整交接

操作者 Codex；基线 68b16df；分支 agent/codex/p0-research-scope。用户授权同步修改介绍与其他文档，本次独占根规范与契约变更。

修改 README/SPEC、ADR、数据模型、接口契约、前端文案及相关任务和验收；历史研究保留并明确范围已更新。业务尚未实现，不声称数据覆盖已经具备。

状态 REVIEW：需独立工程审查后合入。下一位开发者须采用 general_research 请求，支持主体类别与覆盖校验；不要沿用旧年度请求作为唯一入口。

验证：python3 scripts/check_docs.py（32份 Markdown、链接及模拟算术通过）；使用原工作树文档虚拟环境执行 scripts/check_contracts.py（OpenAPI/Schema 与非法输入通过）；web 下 npm ci && npm run build（通过）。未修改 Go 代码，未重复运行 Go 测试；未进行浏览器视觉测试。

## 2026-11-06 同步 dev

用户已建立本分支到 dev 的 PR。将 dev 的 c4467cb 合入本任务分支，保留研究范围修订 b9f3266。README、TASKS 与 docs/README 的同位置追加冲突通过保留两组内容解决；分支规范与 CI 随 dev 合入，不改研究接口与前端实现。

验证：文档/链接检查、OpenAPI/Schema 检查及 git diff --check；无业务代码变更，不重复运行 Go/前端测试。推送后由原 PR 继续审查，未直接合入 dev。
