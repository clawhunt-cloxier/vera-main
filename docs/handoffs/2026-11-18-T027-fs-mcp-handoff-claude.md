# FS→Vera MCP 接收回执与集成设计

**HANDOFF_ID:** FS-VERA-MCP-HANDOFF-20261118  
**日期：** 2026-11-18  
**FROM：** Finance Suite CC002 (`clawhunt-cloxier/finance-suite` 侧)  
**TO：** Vera C002 (`clawhunt-cloxier/vera-main`)  
**源分支：** `feature/trust-gate-mcp-wiring-gap1-gap4` @ `d10e9b7aceea54368c4ee7666c953cd1f123c550`

---

## 一、接收回执

| 项目 | 核验结果 |
|---|---|
| SOURCE_REPO | `jinny280401-glitch/finance-suite` |
| SOURCE_COMMIT | `d10e9b7aceea54368c4ee7666c953cd1f123c550` |
| TARGET_REPO | `clawhunt-cloxier/vera-main` |
| BASE_BRANCH | `origin/dev` @ `0052ea8e07577e340001bf1c6e33b5dcfdecfba9` |
| HANDOFF_BRANCH | `agent/claude/T027-fs-mcp-handoff-20261118` |
| FS GAP1_MCP_WIRING | PROVEN (C-02/C-03/C-04 all pass) |
| FS GAP4_NEGATIVE_TESTS | PASS (10/10) |
| FS OPEN_DEFECTS | 5 (OD-1~5，已登记，未关闭) |
| IMPLEMENTATION_AUTHORIZED | NO |
| PRODUCTION_DEPLOYMENT | NO |

---

## 二、Finance Suite MCP 能力与 Vera Data Service 对照

Vera 的数据服务层（`docs/DATA_SERVICE.md`）定义了统一适配器接口（`Provider` / `Mapper`），通过 `dataset_id` 注册数据集，所有取数经数据服务流转，不直接访问厂商接口。Finance Suite 以 MCP 工具暴露数据能力，**不是 Vera 数据服务 `Provider` 接口的原生实现**。

Vera 接入 Finance Suite 的正确路径是：**为 Finance Suite MCP 编写适配器**，将其包装为实现 `Provider` 接口的 Go 适配器，输出 `RawPage`，再经 `Mapper.Normalize` 映射至 Vera 统一数据集。

```
Vera Harness
  ↓
数据服务 (Go Provider 接口)
  ↓
FinanceSuiteMCPProvider (新适配器，待实现)
  ↓  MCP stdio / HTTP
Finance Suite mcp_server.py
  ↓
wind_query / stock_analysis / ...
```

### 数据集对照（SPEC.md dataset_id → FS 工具）

| Vera dataset_id | Finance Suite 工具 | FS Trust Gate | 映射可行性 |
|---|---|---|---|
| `mkt.daily_bar` | `wind_query(action="stock")` | WIRED (G2+G3) | PARTIAL — EOD 快照，非完整日线 |
| `fin.income_stmt` / `fin.balance_sheet` | `wind_query(action="financials")` | WIRED | PARTIAL — 多源降级，完整性 NOT_PROVEN |
| `fin.consensus` | `wind_query(action="consensus")` | WIRED | PARTIAL — Wind/Tushare NOT_PROVEN |
| `fin.valuation` | `wind_query(action="valuation")` | WIRED | PARTIAL — OD-4 open，derived 状态 |
| `ref.security` | `stock_analysis` / `watchlist_manage` | PARTIAL | PARTIAL |
| `mkt.intraday_quote` | 无对应（实时行情 NOT_PROVEN） | — | NOT_AVAILABLE |
| `fin.shareholders` | `wind_query(action="shareholders")` | WIRED | PARTIAL — Wind 单源 NOT_PROVEN |
| `news.flash` | `sinafinance_fetch` | QC only | PARTIAL — 新闻参考 |

---

## 三、缺口矩阵

| 缺口 ID | 描述 | 影响 Vera 功能 | 修复责任 | 优先级 |
|---|---|---|---|---|
| GAP-V1 | FS MCP 无原生 Vera `Provider` 接口适配器 | 全部数据集接入 | Vera 侧实现 FinanceSuiteMCPProvider | P0 |
| GAP-V2 | G1 Source Identity 未实现（`source_used` 自报）| evidence 来源可信度 | FS 侧独立 Sprint | P1 |
| GAP-V3 | OD-4/OD-5：`stock_analysis` derived valuation QC 升级 | fin.valuation 数据等级误判 | FS 侧 OD-4/OD-5 修复 | P0 |
| GAP-V4 | Wind/Tushare 实际连接 NOT_PROVEN | 生产数据可用性 | FS 侧 provider 验证 | P1 |
| GAP-V5 | 非 wind_query/stock_analysis 工具无 Trust Gate | 其他数据源准入 | FS 侧 Sprint | P2 |
| GAP-V6 | FS HTTP `/api/analyze` 路径无 Trust Gate（F-PROD-BOUNDARY-01）| 若 Vera 通过 HTTP 调用则绕过 Gate | FS 侧独立 Sprint | P1 |
| GAP-V7 | OD-1：未来 date-only 时间戳 G3 容差过宽 | 时间戳异常数据可进入 LLM | FS 侧 OD-1 修复 | P1 |

---

## 四、Vera 集成最小建议（设计，不授权实现）

### 4.1 适配器骨架（Go 伪代码）

```go
// FinanceSuiteMCPProvider 实现 Provider 接口，包装 MCP 工具调用。
// 仅在 development/test 模式可用，production 启动检测到 fixture 则拒绝。
type FinanceSuiteMCPProvider struct {
    mcpClient MCPClient  // stdio 或 HTTP MCP 客户端
}

func (p *FinanceSuiteMCPProvider) Fetch(ctx context.Context, q DatasetQuery) (RawPage, error) {
    // 1. 将 dataset_id + subjects 映射为 wind_query action + code
    // 2. 调用 mcp_client.Call("wind_query", params)
    // 3. 解析响应：检查 _trust_gate.overall_verdict
    //    - "BLOCK" → 返回 NOT_COVERED 或 UPSTREAM_UNAVAILABLE 错误
    //    - "PASS"  → 包装为 RawPage（body_ref=原始 JSON, body_sha256=hash）
    // 4. 不做语义转换（Mapper 负责）
}
```

### 4.2 Trust Gate 结果处理规则

Vera 适配器从 `wind_query` 收到响应后，必须在 `Fetch` 内部处理 Trust Gate 决策，**不得将 BLOCK 状态的响应 body 传递给 `Mapper`**：

```
_trust_gate.overall_verdict == "BLOCK"
  → 记录 reason_code 和 decision_id（审计用）
  → 返回 ProviderError{Code: NOT_COVERED or UPSTREAM_UNAVAILABLE}
  → Harness 按 SPEC §错误分类处理（章节标 limited 或 blocked）

_trust_gate.overall_verdict == "PASS"
  → content（`\n\n` 之后的部分）封装为 RawPage
  → 进入 Mapper.Normalize
```

### 4.3 evidence 追溯

每个 `RawPage.request_fingerprint` 应包含 `_trust_gate.decision_id`（格式 `tg-wind_query-{ts}-{hex}`），以便 Vera 研报的证据侧栏可追溯至具体 Trust Gate 决策记录。

### 4.4 D14 License Gate 兼容

FS MCP 工具返回的数据包含 Wind/Tushare/AkShare 来源数据。Vera D14 License Gate 要求数据来源有明确授权。在 Wind/Tushare 实际连接 NOT_PROVEN 阶段，`FinanceSuiteMCPProvider` 应标注 `license_status: UNVERIFIED`，D14 Gate 据此决定是否放行进入报告。

---

## 五、FS Trust Gate 遗留缺陷对 Vera 的影响（摘要）

详见 Finance Suite 仓库 `docs/FS_TRUST_GATE_OPEN_DEFECTS.md`。

| 缺陷 | Vera 侧影响 | 建议 Vera 侧缓解措施 |
|---|---|---|
| OD-4/OD-5（P0）| stock_analysis derived valuation 时 status 误升为 success | Mapper 检查 `valuation_source.type`；derived → 记录 `quality_issue: DERIVED_VALUATION` |
| OD-1（P1）| 未来 date-only 时间戳可 PASS G3 | Mapper 额外校验 `trade_date <= today`；异常时记录 quality_issue |
| OD-2（P2）| daily_price 契约不一致 | Mapper 不依赖 FS 侧 TIMESTAMP_REQUIRED_ACTIONS |
| OD-3（P3）| 仅测试问题，gate 逻辑正确 | 无需 Vera 侧处理 |

---

## 六、Vera 可用 / 暂不可用工具速查

### 条件可用（Trust Gate 已接线或准入 PARTIAL）

- `wind_query`（G2+G3 接线）：读 `_trust_gate.overall_verdict` 决定消费
- `stock_analysis`（build_evidence_bundle+authorize_task PARTIAL）：须传 `requested_use`

### 条件可用（无 Trust Gate，仅参考）

- `macro_snapshot`、`market_pulse`、`sinafinance_fetch`：宏观/盘面/新闻参考
- `research_reports`、`research_digest`：研报链接/脱水
- `watchlist_manage`：自选股元数据
- `jqdata_query`、`ths_query`、`emquant_query`：延迟/未验证来源

### 暂不可用

- `factor_scan`：JQData 试用账号延迟，实时信号不可靠
- `zhihu_fetch`、`barchart_fetch`：需 Chrome 环境
- `market_intel`：内部聚合，非直接调用设计

---

## 七、下一步（NEXT_GATE = OWNER_REVIEW）

1. Owner 审查本 Handoff 文档
2. Vera 侧启动 GAP-V1（FinanceSuiteMCPProvider 适配器）实现，须 Owner 授权
3. FS 侧修复 OD-4/OD-5（P0），须 Owner 授权
4. 完成后进入 Vera×FS 集成冒烟测试（fixture 模式）

**IMPLEMENTATION_AUTHORIZED = NO**  
**PRODUCTION_DEPLOYMENT = NO**
