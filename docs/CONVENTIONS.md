# 代码与目录约定

状态：当前规范。目的是让不同 Agent 写出的代码结构一致、可以互相接手。与 AGENTS、CONTRIBUTING 冲突时以它们为准。

## 1. 目录

```text
backend/
  cmd/
    api/            # vera-api
    worker/         # vera-worker（River 作业）
    admin/          # vera-admin
    harness/        # vera-harness（replay、eval、adapter test）
  internal/
    platform/       # 配置、日志、错误、幂等、审计、限流、数据库与事务工具
    identity/       # OIDC、会话、邀请、workspace
    catalog/        # 实体目录、能力登记
    dataservice/
      provider/     # Provider 接口与注册
      providers/<id>/   # 每个厂商一个包；fixture、replay 也在这里
      mappings/<id>/    # 映射 YAML 与样本
      records/      # 统一记录读写
      snapshot/
    harness/
      skill/        # 技能注册
      skills/<skill_id>/  # 技能定义、章节、提示词（prompts/*.md）
      facts/        # 公式与事实表
      model/        # 模型网关与 profile
      rules/        # R-01–R-09 确定性检查
      pipeline/     # 步骤执行、状态机推进
    research/       # 任务命令、配额、取消、交付
    reports/
    watchlist/
    feedback/
    httpapi/        # 路由与 handler，只做编解码和调用应用服务
  migrations/       # goose，文件名 NNNN_<desc>.sql，编号串行分配
  queries/          # sqlc 查询，按模块分文件
web/src/
  routes/           # TanStack Router 文件路由
  api/              # 生成的类型与 Query hooks
  components/
  report/           # 报告渲染组件
deploy/             # compose、caddy、keycloak realm、配置样例
tests/fixtures/     # 入库的模拟数据与回放录制
```

每个后端模块内部分 `domain`（类型与规则，不依赖外部包）、`app`（用例）、`store`（sqlc 适配）三层；模块之间只通过 `app` 层的接口调用。

## 2. Go

- 依赖锁定在 go.mod；新增依赖须在 PR 中说明理由与许可。基础库：chi、pgx/v5、sqlc、goose、River、shopspring/decimal（含 pgx 适配）、santhosh-tekuri/jsonschema、coreos/go-oidc、golang.org/x/oauth2。T011/T012 首次引入时锁定版本。
- 金额与比例只用 decimal；除法必须指定精度（内部 18 位小数），展示时按 half-up 舍入。禁止 float64 进入业务计算。
- 所有外部调用（数据库、厂商、模型）接收 `context.Context` 并设置超时。
- 错误：内部用带 code 的错误类型（`platform/errs`），handler 统一映射为 API 错误信封；不向用户返回内部错误文本。
- 时间：存储与计算一律 UTC；交易日与报告期用 `civil.Date` 风格的日期类型，不用带时区的时间表示日期。
- ID：UUIDv7。
- 测试：单元测试与包同目录；需要数据库的测试用 `//go:build integration` 标记，`make test-integration` 启动 compose 中的 PostgreSQL 运行。

## 3. 提示词与技能资产

- 提示词放在 `harness/skills/<skill_id>/prompts/`，中文 Markdown，文件头写版本号；通用规则放在 `harness/skills/_common/prompts/`。
- 构建时计算每个提示词文件的哈希，写入 `model_calls` 与报告版本。
- 修改提示词、章节、规则或公式：升级技能版本，更新 docs/skills/ 中的规格，重新录制受影响的 replay 样本，运行 `vera-harness eval --suite fixture`，在 PR 中附结果。
- 规则集（禁用表达表、数字匹配规则）为 `harness/rules/*.yaml`，同样版本化。

## 4. 前端

- TypeScript strict；ESLint + Prettier（配置在 `web/eslint.config.js`、`web/.prettierrc.json`，`make lint-web` 检查）。Go 使用 golangci-lint v2（`.golangci.yml`，`make lint-go`）。
- 服务端数据只通过 TanStack Query 获取；不引入全局状态库，除非 PR 说明理由。
- 样式用 CSS Modules 与 `src/styles/tokens.css` 中的变量；后续换设计系统时只替换 tokens 与组件样式。

## 5. 日志与审计

- slog JSON 输出，必备字段：`ts`、`level`、`msg`、`request_id`、`workspace_id`（有则填）、`task_id`、`step_key`、`component`。
- 不记录：cookie、token、密钥、报告正文、厂商原始响应正文、用户一句话输入原文（记录长度与哈希）。
- 审计事件：登录、邀请兑换、任务创建/取消/交付、管理 CLI 操作、厂商配置变更。

## 6. 配置

- 环境变量只放密钥引用、地址与开关（见 `.env.example`）；其余配置在 `deploy/config/<env>.yaml`，启动时用 Schema 校验，缺项拒绝启动。
- `VERA_ENV=production` 时拒绝：fixture/replay 适配器、关闭 Secure cookie、缺少价格表的模型 profile、缺少许可确认的数据集。
