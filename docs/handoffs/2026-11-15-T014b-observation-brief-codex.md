# T014b 真实成交数据简报接线与验收

- 操作者：Codex；分支 `agent/codex/T014b-emquant`，基线 `4b2025d` / dev `4abe647`。
- 用户确认：允许先交付休市真实简报，盘中再验证实时。范围依 ADR-0005 补充；不宣称完整个股研报、V02 或 0.4 实时协议完成。

## 已实现

- `stock_analysis/1.1.0-demo.2`：Choice 当前证券身份与 VOLUME/AMOUNT/TURN，经已确认单位转换；请求至采集日前一自然日，表格最后至多二十个日期，模型聚焦最近三个日期。
- 原始响应持久化至 S3，来源许可登记、不可变任务清单、归档哈希核验、workspace/epoch 绑定；重试不替换首个冻结清单。
- 存储、模型和展示按用途重新授权。模型实际发送/重试在取得并发槽后持有许可锁，不在预算预留或排队时占用来源事务。HTTP 先生成响应，最终重新授权后输出，避免嵌套数据库连接耗尽。
- 新增迁移 00018、显式管理命令 `vera emquant init-brief`，不从 SDK 登录成功推断授权，不重授旧许可。
- 独立 `application/vnd.vera.observation.v1+json`；旧客户端不能确认新模式，0.4 保持不可用。前端协商并检查版本。
- 报告为 limited、非实时，展示采集时点、最后取得日期、来源、证据定位及换手率转换；支持 PDF。模型不接收未验明 TIME/状态/延迟/财报版本，不混入 fixture。
- 展示授权撤销后拒绝目录、列表、报告、证据、PDF 和缓存响应；取消仍可执行并返回无内容 204，幂等重试也可用。真实数据库禁止切回 fixture 启动。
- 真实数据模式只开放已配置证券的个股简报。测试环境仍为开发登录，不等于生产发布。

## 实际真实调用证据

2026-11-15 UTC，显式 `VERA_LIVE_BRIEF=1 go test ./internal/app -run TestLiveObservationBrief -count=1 -v` 通过（初次调用；最终提交复验及网站验收另见下文）。

- 标的：东方财富 300059.SZ；Choice 原生 SDK、持久 SeaweedFS S3、DeepSeek `deepseek-flash`。
- task：`01a1dfb1-a9bd-7cf2-a2e3-eee2c637a4a6`，synthetic=false，limited/非实时，4 次模型调用、10 条通过校验的结论，最后取得日成交日期 2026-11-13。
- 原报告及日志仅保留于操作机私有目录，不提交厂商数据、凭证和授权原文。此次证明真实采集到报告的链路；不代表网站已切换。

2026-11-15T15:27 UTC，提交 `34da841` 再次真实验收通过：task `01a1dfd2-0a75-77e8-b86b-9111b087cec1`，4 次 deepseek-flash 调用、6 条均带证据的结论，synthetic=false、limited，来源只有 Choice，最近日期仍为 2026-11-13。28.80 秒完成。一次本地 SSH S3 转发失效导致采集失败，恢复转发后通过，未修改数据或跳过归档。

## 独立工程复审

由 `/root/review_brief` 独立只读检查当前改动（同 GitHub 操作账号，不伪造独立账号 approval）。范围包括契约、迁移、冻结绑定、授权、模型发送、HTTP/幂等、目录范围与部署。

发现并修复：旧来源聚合污染、TURN 证据缺失、模型排队/重试授权过期、缓存目录与列表权限、取消被展示授权阻断及取消缓存重放、同库 fixture 回退，以及模型成功发送后许可事务提交失败被错误释放预算（现记 UNKNOWN 保留预留待对账）。另补实际 HTTP 响应 Schema 校验，修复 Entity.status 缺 unknown 枚举和 Evidence.origin 错误枚举（改用既有 vendor_derived）；复审通过。兼容检查有 1 条提示：响应状态新增 unknown，客户端须容忍未知值，不以 listed/active 冒充验明状态。审查未发现新的迁移/冻结/协议确定阻断。最终测试由主代理串行执行，审查代理不运行 DB 测试。

## 验证进度

- `TestObservationBriefDelivery`：通过，含原生 ABI 合成库→S3 HTTP→DB→计划/确认→模型→报告；重复交付、跨 workspace、旧协议、API/证据/PDF、归档破坏、证券范围、并发读取、撤销、取消与幂等、fixture 回退拒绝。
- `TestAuthorizationRecheckedBeforeEachSend`：通过，重试前撤权不产生第二次上游调用，预留释放。
- 前端 API 类型漂移、lint、format、build；文档、契约、兼容检查；分支清理 9 项与契约兼容脚本 15 项通过。
- 提交 `34da841` 的完整 `go test -race ./...`、`go vet ./...`、Go lint（0 issues）均通过；临时 PostgreSQL 改用 home 磁盘，`GOFLAGS=-p=1` 避免先前 tmpfs 空间不足。此前失败尝试不记为成功。包含 HTTP 契约小修的最终工作树再次通过完整 race/vet/lint，前端类型/lint/format/build 同样通过；PR 与合并提交的远端 Verify 均已通过，见下方记录。

## 后续边界

休市真实简报交付完成。T014b 全范围仍 IN_PROGRESS：盘中实时 TIME/交易状态/延迟及财报版本、全部技能覆盖、正式身份与运维验收仍未完成；盘中独立验收后扩展。当前受控证券仅东方财富 300059.SZ。不要同时启动第二个 Choice 采集进程；当前显式来源许可有效期至 2026-11-30T15:59:59Z，需依据真实授权续期，不自动重授。

## 集成与部署跟踪

- PR #17 最终头 `4e22c19`：Verify [43730794352](https://github.com/clawhunt-cloxier/vera-main/actions/runs/43730794352) 完整通过，bootstrap 17m57s，native-image 2m26s，branch-policy 通过。
- 2026-11-15T15:54:53Z squash 合入 dev：`e9513e6b4ad22e5581a90a6e6dcbe6ad827014aa`。合并树与已验证任务头一致，无额外文件差异。
- 后续文档/部署验收分支 `agent/codex/T014b-observation-acceptance`，不复用已合入任务分支。dev Verify [43732507273](https://github.com/clawhunt-cloxier/vera-main/actions/runs/43732507273) 与 CD [43732507315](https://github.com/clawhunt-cloxier/vera-main/actions/runs/43732507315) 均成功，随后显式登记许可并切换真实模式。
- 服务器已保存原数据库、配置和镜像备份；当前无在途任务；SDK 动态依赖检查通过。浏览器独立测试账号完成完整真实流程，验收见下。

## 网站最终验收（2026-11-15T16:18 UTC / 北京时间 2026-11-16 00:18）

- 网站：<https://vera.touziagent.com>，输入“分析东方财富的成交数据”；当前仅 300059.SZ 开放真实简报。
- 实际运行镜像：`ghcr.io/clawhunt-cloxier/vera-main:sha-e9513e6b4ad2`，digest `sha256:1fac4dd301b8ccf098ed71f22ed3497b256e25670e57294ddef2fa1684705ad7`，healthy。
- 原生 Choice→持久 S3→River worker→deepseek-flash→核验→报告全部由部署服务实际执行，未注入模拟数据。task `01a1e000-8195-71ed-82b7-4d79c21af7c7`，report `b1ee2f1f-7694-5a42-9f9b-e6e88eb3cfc1`（私人验收 workspace，不公开授权报告访问）。
- 数据采集/冻结：2026-11-16 00:18:13 北京时间；最新返回日期 2026-11-13。`synthetic=false`、`limited`、非实时；6 条结论均带证据，来源仅 Choice；4 次 deepseek-flash 调用全部 SUCCEEDED，任务表完成耗时约 7.10 秒（单次，不是 P95）。
- Playwright 浏览器：登录→邀请码开通→一句话→确认计划→任务进度→报告→证据侧栏→PDF 下载通过；前端无 pageerror。桌面 1440×1000、手机 390×844 截图已查看。工具 CUA 浏览器访问服务不可用，因此使用仓库 TESTING 指定的 Playwright 验收。
- PDF 为 3 页 A4、171184 bytes，Poppler 渲染逐页核对：中文可读、表格跨页重复表头、来源/非实时限制/页码完整，无截断。文件、截图、会话、原始数据及秘密仅保存在操作机权限受控目录与持久对象存储，不进入 Git。
- 上线前数据库/配置/镜像备份已保存，真实模式用单个 worker；SDK/S3 凭证来自服务器秘密配置。仍为邀请制测试环境开发登录，不宣称生产身份、安全或全面投研能力验收完成。
