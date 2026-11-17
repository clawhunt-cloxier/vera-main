# T027 报告、证据与显示授权审查

- 任务：T027 数据治理实时研报主链路。
- 审查者：Codex `/root/frontend_realtime`。本代理实现过前端部分及图表媒体类型修复，因此这些部分是作者复核；本次对主代理新增的报告、证据、PDF 和快照授权代码作独立交叉审查。共享账号不构成 GitHub 独立审批。
- 被审查 commit：`2d4e30f484834503a09ef3f8a83420ce27425140`；T027 基线 `0052ea8`。审查开始于 `6ff0c3f`，随后读取 `0d98654` 的 API 输入校验变更；未将该 SHA 之后的未提交修改计入结论。
- 实际读取范围：T027 全量 diff；重点跟踪 `httpapi/server.go`、`research_version.go`、`handlers.go`、`observation_brief.go`、`pdf.go`、`types.go`，`emquant/task_snapshot_use.go`、`task_snapshot.go`、`quote_component_authorize.go`、`quote_view.go`，`dataservice/authorization.go`，`harness/report.go`、`realtime_brief.go`、`quote_facts.go`、计划/确认及交付，`reportpdf/render.go`，对应 OpenAPI、ADR-0004、技能规格及新增测试。
- 实际执行检查：`git diff --check 0052ea8..2d4e30f` 通过；调用链和 SQL 只读审查。按协调要求未运行数据库或 race 测试，未重跑真实 SDK、模型或部署验收。此前前端模拟浏览器和协议测试结果见 T027 交接，不能代替此次后端授权并发验证。

## 发现

1. **P1 — 实时 worker 覆盖了任务预览时点。** `backend/internal/harness/realtime_brief.go:131` 将 `t.AsOf` 直接赋为冻结快照的 `Cutoff`。数据库中的 `research_tasks.as_of` 仍是计划预览时点，但内存中的 task 被用于 writer 的日期上下文和 `harness.Document.AsOf`，因此最终报告的 `as_of` 与同一任务 API 的 `Task.as_of` 不一致；这也让报告无法同时保留 ADR-0004 要求的预览时点与冻结截止时间。应保留 `t.AsOf`，只设置 `t.Snapshot`/`SnapshotRevision`，让确定性检查使用单独的冻结截止上下文，报告通过 `snapshot_as_of`/`data_as_of` 表达冻结时间，并增加数据库/API/报告一致性测试。
2. **P2 — PDF 丢失来源时间精度和部分来源元数据。** `backend/internal/reportpdf/render.go:58` 的 `SourceFreshness` 未接收 `published_at`、`period`、`status`；`render.go:940` 的新增来源表复用 `fmtTime`，而该函数在 `render.go:326` 仅格式化到分钟。例：事件 `14:00:01`、接收 `14:00:02`、可用 `14:00:03` 在导出 PDF 中均成为 `14:00`，与网页/API 的实际秒级时点不一致，无法核对短时延；旧 observation 报告的期间也会丢失。建议来源时点使用独立秒级格式，并完整传递、展示上述字段；增加包含不同秒值与非空期间/披露时间的 PDF 验证样例。
3. **P2 — 反馈写入先于显示授权失败（与 worker 审查一致）。** `backend/internal/httpapi/observation_brief.go:25` 的保护器覆盖 `/reports/` 下所有方法，实时路径在第 59 行先运行处理函数，随后才在第 62 行重新授权。`POST .../feedback` 的数据库插入已经发生时，许可撤销可令请求最终失败；客户端重试可能重复提交。建议只让 GET 报告表示经过显示门禁；反馈仍按 workspace 与已交付结论校验，或者将所需权限校验和写入放入同一事务。该问题不造成响应中的原始行情泄露，但会造成可见结果与持久化副作用不一致。

## 已核对边界

- `WithTaskSnapshotQuote` 首先按 workspace、task、epoch、manifest hash 取绑定清单的原始 revision；随后 `ReadBoundSnapshotQuote` 重新核验清单、计划、quote component 和用途许可，不按当前进度 revision 或当前行情重建快照。
- 实时报告 JSON、PDF 和 series 先在内存中组装，最终外部写出放在 `SourceDisplay` 授权回调内。`ReauthorizeQuoteComponent` 锁定当前许可、provider、policy 和任务，验证对象哈希与不可变字段；`server.go` 对这些读取不再套提前释放锁的外层缓冲。未发现本次新增的显示权限绕过。
- 证据读取先通过 workspace 范围内的已交付报告及 `evidence_refs` 定位 task，再在已授权冻结 quote 中匹配 `StageID`。响应仅含规范化价格、状态、时间、内容哈希和字段定位，不返回 `RawRef`、S3 URL、凭证或完整原始 SDK 响应。无对应报告时不会直接按归档对象 ID 读取。
- 旧 JSON 表示在写出时删除新快照/行情时间字段；observation 保留其原先字段并移除 `source_freshness`。0.4 可读取旧 observation 计划和报告，旧计划 `quote_requirement=null` 仍需 observation 协议确认；PDF 独立采用 `application/pdf`，不经 JSON 字段过滤。
- 旧 observation 路径的 `WithDisplayCatalog` 当前依基线实现为 SDK 凭证授权、不查 `source_licenses`；本次不能将它描述为与实时路径相同的逐次用途授权。T027 新实时报告/证据路径独立调用现行许可检查，该历史差异不构成本次新增绕过。
- 未看到 SourceFreshness 或显示路径允许模型决定数据范围、时间或授权。`fresh` 表示冻结时通过策略，文案明确绑定冻结时点；历史报告不会因读取时行情已变旧而重新取数。

## 结论

`2d4e30f` 仍需修复以上 P1/P2 项；在实际读取范围内未发现新的跨 workspace、原始对象 URL/凭证泄漏或实时数据用途授权绕过。现有新增自动测试主要覆盖 Harness、HTTP 计划确认和前端模拟响应；报告/PDF/证据撤权后的 HTTP 行为与真实厂商盘中条件仍须分别验证，不能凭本次只读审查宣称通过。
