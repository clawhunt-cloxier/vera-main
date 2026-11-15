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

2026-11-15 UTC，显式 `VERA_LIVE_BRIEF=1 go test ./internal/app -run TestLiveObservationBrief -count=1 -v` 通过（随后有权限/并发回归修正，网站验收仍待完成）。

- 标的：东方财富 300059.SZ；Choice 原生 SDK、持久 SeaweedFS S3、DeepSeek `deepseek-flash`。
- task：`01a1dfb1-a9bd-7cf2-a2e3-eee2c637a4a6`，synthetic=false，limited/非实时，4 次模型调用、10 条通过校验的结论，最后取得日成交日期 2026-11-13。
- 原报告及日志仅保留于操作机私有目录，不提交厂商数据、凭证和授权原文。此次证明真实采集到报告的链路；不代表网站已切换。

## 独立工程复审

由 `/root/review_brief` 独立只读检查当前改动（同 GitHub 操作账号，不伪造独立账号 approval）。范围包括契约、迁移、冻结绑定、授权、模型发送、HTTP/幂等、目录范围与部署。

发现并修复：旧来源聚合污染、TURN 证据缺失、模型排队/重试授权过期、缓存目录与列表权限、取消被展示授权阻断及取消缓存重放、同库 fixture 回退，以及模型成功发送后许可事务提交失败被错误释放预算（现记 UNKNOWN 保留预留待对账）。审查未发现新的迁移/冻结/协议确定阻断。最终测试由主代理串行执行，审查代理不运行 DB 测试。

## 验证进度

- `TestObservationBriefDelivery`：通过，含原生 ABI 合成库→S3 HTTP→DB→计划/确认→模型→报告；重复交付、跨 workspace、旧协议、API/证据/PDF、归档破坏、证券范围、并发读取、撤销、取消与幂等、fixture 回退拒绝。
- `TestAuthorizationRecheckedBeforeEachSend`：通过，重试前撤权不产生第二次上游调用，预留释放。
- 前端 API 类型漂移、lint、format、build；文档、契约、兼容检查；分支清理 9 项与契约兼容脚本 15 项通过。
- 最终 `go test -race ./...` 正在执行，临时 PostgreSQL 改用 home 磁盘，`GOFLAGS=-p=1` 避免先前 tmpfs 空间不足。此前失败尝试不记为成功。

## 下一步与边界

1. 完成最终 race/lint 与远端 Verify，更新确切提交与测试结果。
2. PR squash 合入 dev，经 CI/CD 部署新镜像；显式登记来源许可，停止旧 worker 后切换真实模式。不能同时运行两个 Choice 账号进程。
3. 网站完成真实用户流程、证据和 PDF 验收，记录镜像 SHA、报告 ID、采集日期；届时才确认用户可试用。
4. 实时 TIME/交易状态/延迟及财报版本、全部技能覆盖、正式身份与运维验收仍未完成；盘中独立验收后扩展。
