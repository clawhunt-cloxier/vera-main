# MVP 交接：前端契约对齐、真实模型联调与 demo

- 操作者：Claude Code；分支：agent/claude/mvp；未 push。
- 时间：2026-11-06 至 07 UTC。
- 状态：REVIEW。MVP 主链路已用真实模型在演示数据上跑通一次，仍有未验证项和未修问题，见下。

## 本轮做了什么

1. 前端路由树改为由 `@tanstack/router-plugin` 在构建时生成，替换了手写的 `routeTree.gen.ts`（手写版有重复 `__root__` id，是黑屏的原因）。`package.json` 恢复为与锁文件一致的精确版本，`build` 重新包含 `tsc --noEmit`。
2. 前端与 `contracts/openapi.json` 对齐：报告序列、证据、计算、反馈的路径与请求体；技能列表结构；计划结果 `plan|choose|unsupported`；取消任务带 `expected_revision`；自选股改用 DELETE 与字符串数值；登录退出改为 POST；深度研究模式只提供契约允许的取值。计划页不允许取消必需章节，`executable=false` 时不可提交。删除了没有后端接口的提醒页路由。
3. 后端：`vera harness` 补建只插入的 River 客户端（原先 nil 指针崩溃）；`admin task list`；开发登录页 `pattern` 转义；演示横幅不再声称模型一定是模拟的。
4. 真实模型联调（OpenAI 兼容接口，`deepseek-flash`）：
   - 默认开启思考时，思考内容耗尽 `max_tokens`，JSON 被截断，必需章节失败，任务按设计弃权。通过 `MODEL_<PROFILE>_EXTRA_BODY='{"thinking":{"type":"disabled"}}'` 关闭思考。
   - 输出上限提高：写作 8000、核验 4000、摘要 4000。
   - `contracts/examples/policy.json` 的单任务预算放宽（仅为演示，上线前必须收紧）。

## 已验证（有命令或结果可复现）

- `npx tsc --noEmit`：0 错误；`npm run build` 通过。
- 无头 Chromium（系统 `/usr/bin/chromium`，Playwright 驱动）对 18094 实例：开发登录、邀请码开通、工作台、自选股页、报告列表；自由文本发起个股分析 → 计划页 → 提交 → 任务页轮询到完成 → 报告页；4 个 SVG 图表（ECharts 使用 SVG 渲染，不是 canvas）；点开证据面板后证据与计算接口均返回 200；反馈弹窗字段为 `kind`、`comment`。过程中无控制台报错、无 4xx/5xx。
- 真实模型一次个股分析（DM0001.DEMO）：COMPLETED，约 50 秒，21 次模型调用，约 0.03 美元；被核验层删除 4 条结论（3 条由确定性规则 R-02、R-05、R-08 拦下，1 条由语义核验判为夸大）；一次 writer 调用失败、一次 UNKNOWN 后重试成功。

## 未验证

- 资金动向、深度研究在真实模型下的结果。
- 反馈实际提交、任务取消、自选股添加与移除、快速启动页、`choose` 分支，只做了类型对齐，没有在浏览器里走过。
- 本轮改动后没有重新运行 `go test`；之前的 Go 测试结果不能当作本轮结论。
- 浏览器脚本只在作业临时目录里，没有进入仓库，尚无正式的 Playwright 用例。

## 未解决的问题

1. `harness/write.go` 在语义核验失败时仍保留部分事实性内容并标记已核验；`harness/report.go` 的摘要只经确定性规则检查，没有语义核验。审阅意见里的这两点仍然成立。
2. `allow_model_processing`、`VERA_REGISTRATION_ENABLED`、`VERA_REAL_RESEARCH_ENABLED` 只被存储或读取，没有被强制执行，接真实厂商与模型前必须补上。
3. `MODEL_<PROFILE>_THINKING` 配置项被读取但没有写入请求，只有 `EXTRA_BODY` 生效。
4. `vera harness eval` 不能在有存活 worker 的库上运行：worker 会先领走任务，eval 只会看到中间状态。
5. 输出上限是硬编码，没有配置化。
6. `admin usage report` 默认日期与实际调用日期可能因时区差一天，需要用 `--day` 指定。
7. 数据源仍是 fixture，只要数据源是合成的，演示横幅就会显示；用真实模型时，报告内容仍基于虚构数据。
8. `README-MVP.md` 里有未经验证的表述，需要重写成只含已验证内容。
9. River 周期任务的注册方式没有核实是否需要额外的 `PeriodicJobs` 配置。

## 运行中的演示实例（本机，非持久）

- 18093：模拟模型。
- 18094：`deepseek-flash`，关闭思考，日费用上限 20 美元，使用放宽后的预算。
- 两者都用 `vera demo` 脱离 shell 启动，数据在作业临时目录，不是持久数据。

## 密钥

模型 API 密钥是用户在对话里提供的，只通过进程环境变量使用，没有写入任何文件或提交（提交前已扫描暂存区）。该密钥已出现在对话记录里，建议联调结束后轮换。

## 下一步

1. 修上面第 1、2 项，再补第 3 项。
2. 把浏览器脚本整理成仓库里的 Playwright 用例，并接入 `make verify`。
3. 在真实模型下跑资金动向与深度研究，检查弃权与受限结果的展示。
4. 重跑 `make test-go`、`make build-web`，重写 README-MVP.md，再开 PR。

## 更新（同日晚）

已处理：

- 核验降级：语义核验不可用时，章节不再标记为已核验，并在报告的数据与限制中披露（044e24f）。摘要仍只经确定性检查，没有语义核验，这一点仍然开放。
- `allow_model_processing`、`VERA_REGISTRATION_ENABLED`、`VERA_REAL_RESEARCH_ENABLED` 已强制执行，并有三个负向测试（ec7489c）。`vera demo` 默认开启注册。
- harness 包此前的测试失败，原因是 /tmp 这个 tmpfs 报 Disk quota exceeded，不是代码问题。把 `VERA_TEST_PG_DIR` 指到有空间的磁盘后，`go test ./...` 全包通过。
- 浏览器补验：快速启动、提交后取消（最终为 CANCELLED，原因 USER_CANCELLED）、自选股添加、分析、移出、反馈提交（接口 201，界面已修为提交后保留确认提示，1eb8a09）。
- 真实模型（deepseek-flash）下三个技能各跑一次：个股分析完整；资金动向、深度研究完成但标为受限。
- README-MVP.md 已重写为只含已验证内容。

仍然开放：

1. 摘要没有语义核验。
2. `MODEL_<PROFILE>_THINKING` 未接入请求；输出上限仍写死在代码里。
3. `vera harness eval` 不能在有存活 worker 的库上运行。
4. `choose` 分支没有在浏览器里走过；仓库里没有 Playwright 用例。
5. `contracts/examples/policy.json` 的预算为演示放宽，试点前必须收紧。
6. 模型密钥出现在对话记录里，联调结束后需要轮换。

## 更新：报告导出 PDF

- 新增 `GET /api/v1/reports/{id}/versions/{version}/pdf`，已登记到 OpenAPI，并在 `TestAPIFlow` 中覆盖。前端报告页新增下载入口。
- 排版在 `backend/internal/reportpdf`。canvas 对思源黑体（CID-keyed CFF）的子集化不可用，于是把 `tdewolff/font` 以 `replace` 方式放进 `backend/third_party/tdewolff-font`，补了三处：多 Font DICT 的 CFF 子集化与写出；`cntrmask` 对隐含 vstem 的计数（上游缺陷，导致大部分汉字解析失败）；cmap format 4 写出（原实现会产生非法 idRangeOffset）。补丁见该目录下 `PATCHES.diff`，建议之后向上游提交。
- 字体文件约 17MB，直接提交进仓库（没有 Git LFS）。仓库此前最大的文件只有 92KB，请审阅时留意。
- 已知排版经验：canvas 的 `TextOptions.Indent` 会把缩进插在首字符之后，所以不要使用；居中对齐拉丁字母混排的粗体标签会叠字，标签改为左对齐。
- 未验证：超长表格、几十页报告的视觉效果；没有 PDF 视觉回归测试，目前只有结构性断言。
