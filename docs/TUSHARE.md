# TuShare Pro 收盘研究

工作台可对已登记的 A 股公司运行个股分析、个股资金动向和公司深度研究。技能版本为 `1.0.1`。研究计划冻结数据库的可用截止时间，行情取最近已收盘交易日，财报同时受实际披露时间和本地入库时间约束。需要更新时，由运维发起同步。模型只使用计划中已有的证据和计算结果。

## 接口与覆盖

| 研究需要 | 实际调用 | 交付条件 |
| --- | --- | --- |
| 个股、基准和行业表现 | `stock_basic`、`trade_cal`、`daily`、`adj_factor`、`daily_basic`、`suspend_d`、`index_daily`、`index_member_all`、`sw_daily` | 约五年历史，日成交量由手转股、成交额由千元转元。以沪深 300 和有效申万二级行业为基准，收益使用可比日期。 |
| 估值与同行 | `daily_basic`、`index_member_all` 及同行财报 | 仅选择同期有市值的同行，最多八家。缺少五家有效同行时披露比较限制。 |
| 财务、业务和风险 | `income`、`balancesheet`、`cashflow`、`fina_mainbz`、`anns_d` 及登记来源的年报 PDF | 连续两个可比年度的合并财报与对应公告。不同分部维度分别计算。正文按 PDF 页码和段落定位。无法定位对应披露版本时省略有关事实。 |
| 资金与股东 | `margin_secs`、`margin_detail`、`moneyflow`、`block_trade`、`stk_holdernumber` | 两融资格、融资余额和同日流通市值分别核对。资金分档为厂商加工口径。缺少必需的杠杆资金数据可导致资金报告无法执行。 |
| 近期事件 | `anns_d`、`major_news` | 新闻仅关联标题包含该证券名称的报道，注明媒体。当期没有授权新闻时，以近期公告支持剩余内容。 |

接口定义可公开查阅。用户登录后可查看自己的 Token、积分和独立订阅。请按部署账号实际返回验收，特别核对公告和新闻。参见 [官方 HTTP 说明](https://tushare.pro/document/1?doc_id=130)、[积分与接口](https://tushare.pro/document/1?doc_id=108)、[独立权限](https://tushare.pro/document/1?doc_id=290)、[日线](https://tushare.pro/document/2?doc_id=27)、[合并利润表](https://tushare.pro/document/2?doc_id=33)、[公告](https://tushare.pro/document/2?doc_id=176)。

## 配置与运行

1. 在部署的密钥管理或权限为 0600 的环境文件中配置 `TUSHARE_TOKEN`。API、worker、同步和管理进程使用同一数据库和同一配置。生产环境还需已有的 OIDC 与真实模型配置。变量名见 [部署模板](../deploy/.env.example)。
2. 设置 `VERA_DATA_PROVIDER=tushare`、`TUSHARE_SUBJECTS=600519.SH`（示例，可用逗号登记多只），配置 `TUSHARE_S3_ENDPOINT`、`TUSHARE_S3_REGION`、`TUSHARE_S3_ACCESS_KEY`、`TUSHARE_S3_SECRET_KEY` 与 `TUSHARE_S3_BUCKET`。使用独立测试数据库或已有真实数据环境。已有模拟实体与真实实体证券代码冲突时，发布会失败并保留原数据。
3. 确认许可覆盖本部署的存储、展示及模型处理用途后，设置 `TUSHARE_ALLOW_MODEL_PROCESSING=true` 与 `VERA_REAL_RESEARCH_ENABLED=true` 并重启相关进程。可在授权尚待确认时单独采集归档。模型用途默认关闭。
4. 应用宿主需能通过 TLS 连接 `api.tushare.pro:443` 和公告文件的登记主机。运行镜像已包含 `pdftotext` 和 `prlimit`。请求身分凭证只发往官方 HTTPS 数据接口。公告下载使用独立请求。原始响应、脱敏请求清单和 PDF 按 SHA-256 归档，失败的同步可重试。

在使用外置数据库的部署中执行以下命令。

```sh
vera admin provider test tushare
vera sync
vera admin provider list
vera admin provider model-use tushare on
```

第一条只查配置中第一家公司的股票名录。同步会按标的采集及发布，并输出新增行数和缺数说明。一次同步覆盖标的、同行与对应披露。建议按账号配额安排收盘后的同步，`TUSHARE_MIN_INTERVAL` 的默认值为 500ms。同一数据库内的进程共享限频和冷却状态。部署或运维关闭用途后，后续同步不能自行恢复授权。使用 `vera admin provider model-use tushare off` 可在数据库即时撤销模型研究与相关展示。生产勿运行开发登录命令 `vera demo`。

## 用真实账号验收

选择一家有五年日线、至少两个连续可比年报、有效两融披露以及足够申万二级同行的非金融 A 股公司。先查同步日志和厂商限制，再从工作台搜索标的，分别提交三份报告。记录报告版本与截止时间，核对复权曲线、最新两融日、年报页码、同比计算输入和分部口径，打开证据并导出 PDF。无权或无覆盖时，记录受限章节或无法执行的原因。由模拟模型生成的报告带模拟标识，真实数据交付验收使用配置好的真实模型。

本轮范围是可追溯的公司收盘研究。盘中报价、行业或全市场汇总、跨厂商自动择源和热备仍是后续工作。公募及互联互通持仓、机构观点、业绩说明会纪要等还需单独的数据接入。目标报告的对照基线见 [早期 MVP](../README-MVP.md)。
