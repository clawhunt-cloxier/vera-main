# Vera MVP 体验与运行说明

更新：2026-11-07。分支 agent/claude/mvp，未 push。本文只写实际跑过的内容；没跑过的单列在第 6 节。

## 1. 重要前提

- 数据是虚构的演示数据（fixture：12 只演示证券、2 个行业），报告不对应任何真实公司或市场，页面顶部有横幅提示。
- 模型可以是模拟模型（无需密钥），也可以是真实模型。目前只联调过 DeepSeek 的 deepseek-flash。真实模型下，写作与核验的文字由真模型生成，依据的仍是虚构数据。
- 登录是开发登录：任意小写用户名，无密码校验。只能在本机使用，不要暴露到网络。

## 2. 构建

```bash
cd backend && go build -o ../vera ./cmd/vera
cd ../web && npm ci && npm run build
```

需要 Go 1.27.x、Node >=26.13 <27。

## 3. 启动

必须在 backend/ 目录下启动，因为默认从 ../contracts 读取契约和策略。启动时终端会打印 3 个邀请码，每个只能用一次。

### 3.1 模拟模型（无需任何密钥）

```bash
cd backend
VERA_API_ADDR=127.0.0.1:18093 VERA_WEB_DIR=../web/dist VERA_DEMO_DIR=$HOME/.cache/vera-demo-sim VERA_DEMO_PG_PORT=55432 ../vera demo
```

### 3.2 接 DeepSeek（真实模型）

密钥只通过环境变量传入，不要写进任何文件。必须关闭思考，否则思考内容会耗尽输出上限，JSON 被截断，必需章节失败，任务弃权。

```bash
export DEEPSEEK_API_KEY=你的密钥
cd backend
EB='{"thinking":{"type":"disabled"}}'
VERA_API_ADDR=127.0.0.1:18094 VERA_WEB_DIR=../web/dist VERA_DEMO_DIR=$HOME/.cache/vera-demo-ds VERA_DEMO_PG_PORT=55433 \
VERA_DAILY_MODEL_COST_LIMIT_USD=20.00 MODEL_PRICE_TABLE_VERSION=deepseek-2026-11-06 \
MODEL_WRITER_ADAPTER=openai MODEL_WRITER_BASE_URL=https://api.deepseek.com MODEL_WRITER_ID=deepseek-flash MODEL_WRITER_API_KEY=$DEEPSEEK_API_KEY MODEL_WRITER_EXTRA_BODY=$EB \
MODEL_VERIFIER_ADAPTER=openai MODEL_VERIFIER_BASE_URL=https://api.deepseek.com MODEL_VERIFIER_ID=deepseek-flash MODEL_VERIFIER_API_KEY=$DEEPSEEK_API_KEY MODEL_VERIFIER_EXTRA_BODY=$EB \
MODEL_ROUTER_ADAPTER=openai MODEL_ROUTER_BASE_URL=https://api.deepseek.com MODEL_ROUTER_ID=deepseek-flash MODEL_ROUTER_API_KEY=$DEEPSEEK_API_KEY MODEL_ROUTER_EXTRA_BODY=$EB \
../vera demo
```

停止：结束 vera 进程，内嵌 PostgreSQL 随之退出。

## 4. 体验流程

1. 打开 http://127.0.0.1:端口 ，点右上角登录，输入用户名（小写字母、数字、点、下划线、连字符）。
2. 首次登录进入开通工作区页：输入终端打印的邀请码，身份任选。
3. 工作台：在输入框写 `个股分析 DM0001`、`资金动向 DM0001` 或 `深度研究 DM0001`，点开始研究。
4. 计划页：查看章节与可用性，可取消勾选可选章节，点提交任务。
5. 任务页：步骤状态自动刷新。真实模型下个股分析实测约 50 秒。
6. 报告页：S1 摘要、各章节、图表。点结论后的 [n] 打开证据面板（来源、公告编号、修订版本、可用时点、计算过程），点反馈可对结论提交反馈。
7. 顶部的报告入口可查看历史报告。

## 5. 已验证

- `tsc --noEmit` 0 错误，`npm run build` 通过；后端 `go test ./...` 全包通过（测试库放在磁盘：`VERA_TEST_PG_DIR=<有空间的目录>`）。
- 无头 Chromium 实测：开发登录、新用户邀请码开通、工作台、自选股页（空列表）、报告列表；自由文本发起个股分析，经计划、提交、任务完成到报告页；4 个 SVG 图表（ECharts 使用 SVG 渲染，不是 canvas）；证据面板的证据与计算接口均返回 200；反馈弹窗字段与契约一致。过程中无控制台报错、无 4xx/5xx。
- 真实模型（deepseek-flash）三个技能各跑过一次：个股分析完整；资金动向完成但标为受限（S7 厂商资金流整章被核验删除）；深度研究完成但标为受限（S9 外部观点无数据，S6、S7 因缺少业绩说明会纪要而受限）。
- 真实模型用量（各一次）：个股分析约 0.03 美元，资金动向约 0.006 美元。
- 同一套无头浏览器检查还覆盖：快速启动页发起研究、提交后取消任务、自选股添加/分析/移出、反馈提交（接口 201，提交后对话框保持打开并显示确认）。
- 三个开关已强制执行并有负向测试：厂商未授权模型处理时研究取不到其数据；VERA_REGISTRATION_ENABLED 关闭时开通被拒（vera demo 默认开启）；非合成部署未开 VERA_REAL_RESEARCH_ENABLED 时计划与任务被拒。

## 6. 尚未验证

- 需要选择对象的 `choose` 分支：目前只做了与契约的类型对齐，没有在浏览器里走过。
- 深度研究的耗时与费用没有统计。
- 没有仓库内的 Playwright 用例，浏览器检查脚本只在临时目录里。
- 没有在真实厂商数据上运行过。

## 7. 已知限制

- 受限与弃权是预期行为：核验层会删除不合格结论（确定性规则 R-02、R-05、R-08 和语义核验判定夸大）。可选章节被删光，报告标为受限；必需章节被删光，任务弃权。真实模型会稳定地产生这类被删结论。
- 语义核验不可用时，章节只保留经确定性检查的事实与计算结论，不再标记为已核验，并在报告的数据与限制中说明。
- contracts/examples/policy.json 的单任务预算为演示放宽，试点前必须收紧。
- 配置项 `MODEL_<PROFILE>_THINKING` 被读取但没有写入请求，只有 `MODEL_<PROFILE>_EXTRA_BODY` 生效。模型输出上限目前写死在代码里（写作 8000、核验 4000、摘要 4000）。
- `vera harness eval` 不能在有存活 worker 的库上运行，worker 会先领走任务。
- 自选股页的提醒只展示快照里已触发的规则，没有单独的提醒页。

## 8. 运维小抄

```bash
cd backend
export DATABASE_URL='postgres://vera:vera-demo@127.0.0.1:55433/vera?sslmode=disable' VERA_DEV_OIDC=true
../vera admin task list
../vera admin task inspect <任务ID>
../vera admin usage report --day 2026-11-07
```

`usage report` 的日期口径可能与实际调用日期差一天，需要用 `--day` 指定。

## 9. 密钥与安全

- 模型密钥只放环境变量，不写入文件、不提交。
- 开发登录与 vera demo 只用于本机。生产环境会拒绝开发登录以及 fixture、sim、replay。
