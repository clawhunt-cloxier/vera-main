# TuShare Pro 收盘研究

工作台可对已登记的 A 股公司运行个股分析、个股资金动向和公司深度研究。三个技能的新版本均为 `1.0.4`，既有版本的计划与报告语义不变。研究计划固定数据库的可用截止时间，行情取已结束交易日的最近可用日线，财报同时受实际披露时间和本地入库时间约束。正式数据由后台同步更新，也可由运维发起，不在确认时改取计划创建后才入库的版本。

同站启用 `VERA_DATA_PROVIDER=emquant+tushare` 后，三项研究共用普通网页入口，不再选择厂商或行情要求。研究任务尝试通过 Choice 取得近期日成交量、成交额与可用换手率，作为同份报告中的独立观察。缺少补充不影响正式数据判定，不能补写收盘价、财报、杠杆资金等缺失的必需证据。报告对各来源分别列明数据日期、口径和证据；Choice 的日期为厂商序列标签，归档接收时间可能晚于正式数据截止，两者均不代表盘中实时成交或首次披露。历史 observation 简报仍可读取。[双厂商部署](DEPLOYMENT.md)

双厂商部署中，`TUSHARE_SUBJECTS` 是启动时预同步的股票名单。资金动向和公司深研还支持按需采集 Choice 目录中找到的沪深北上市 A 股。在页面选择股票并发起研究后，后台先在 TuShare 核对证券身份，再归档和发布该股数据。发布和登记在同一事务中完成，成功后才创建固定截止计划。动态标的最近 24 小时已发布的批次可复用，报告仍按各数据集的实际日期和覆盖决定可交付章节。仅输入关键词不会触发整股同步。

## 接口与覆盖

| 研究需要 | 实际调用 | 交付条件 |
| --- | --- | --- |
| 个股、基准和行业表现 | `stock_basic`、`trade_cal`、`daily`、`adj_factor`、`daily_basic`、`suspend_d`、`index_daily`、`index_member_all`、`sw_daily` | 约五年历史，日成交量由手转股、成交额由千元转元。以沪深 300 和有效申万二级行业为基准，收益使用可比日期。 |
| 估值与同行 | `daily_basic`、`index_member_all` 及同行财报 | 仅选择同期有市值的同行，最多八家。缺少五家有效同行时披露比较限制。 |
| 财务、业务和风险 | `income`、`balancesheet`、`cashflow`、`fina_mainbz`、`anns_d` 及登记来源的年报 PDF | 连续两个可比年度的合并财报与对应公告。不同分部维度分别计算。正文按 PDF 页码和段落定位。无法定位对应披露版本时省略有关事实。 |
| 资金与股东 | `margin_secs`、`margin_detail`、`moneyflow`、`block_trade`、`stk_holdernumber` | 两融资格、融资余额和同日流通市值分别核对。资金分档为厂商加工口径。缺少必需的杠杆资金数据可导致资金报告无法执行。 |
| 近期事件 | `anns_d`、`major_news` | 新闻仅关联标题包含该证券名称的报道，注明媒体。当期没有授权新闻时，以近期公告支持剩余内容。 |

接口定义可公开查阅。用户登录后可查看自己的 Token、积分和独立订阅。请按部署账号实际返回验收，特别核对公告和新闻。参见 [官方 HTTP 说明](https://tushare.pro/document/1?doc_id=130)、[积分与接口](https://tushare.pro/document/1?doc_id=108)、[独立权限](https://tushare.pro/document/1?doc_id=290)、[日线](https://tushare.pro/document/2?doc_id=27)、[合并利润表](https://tushare.pro/document/2?doc_id=33)、[公告](https://tushare.pro/document/2?doc_id=176)。

资金报告「交易所席位与大宗交易」使用 `top_list` 核对最近 20 个交易日的上榜状态，并通过 `top_inst` 读取上榜原因对应的买卖前五。保留原始证据、榜单覆盖、单日或累计多日的范围及披露版本。没有覆盖的交易日不记为未上榜；不同原因、买卖双榜或多日榜不重复累计为窗口内机构买卖额。更长窗口仅在实际已核对时才展示完整上榜天数。[龙虎榜每日明细](https://tushare.pro/document/2?doc_id=106) · [龙虎榜席位](https://tushare.pro/document/2?doc_id=107)

个股分析的股东与公司事项可展示 `dividend` 已实施、派息日已过且可核对口径的现金分红。每次分配分别保留报告期、登记日、实施公告日、派息日及参与分配的股本。含税每股金额与参与分配的股本折算单次总额，不把样本合计写成全年实际支付。预案不作为已实施事实。[TuShare 分红字段](https://tushare.pro/document/2?doc_id=103)

资金报告的季度持仓章节使用 `fund_portfolio` 展示最近已归档季度的个别公募基金代码、报告期末、实际公告日和持股数。仅展示最多三个按代码排序的披露样例，不按规模排名、不在不同代码间求和。聚合需要区分同一组合的不同份额和前十大与全部持仓，并核对当季股本分母。陆股通仍缺同时可核验季末持股数与实际披露日的季度数据。港交所自 2024-08-19 起在每季第五个北向交易日公布上季末持股。[港交所查询说明](https://www3.hkexnews.hk/sdw/mutualmarket/SH.htm) · [TuShare 公募持仓字段](https://tushare.pro/document/2?doc_id=121)

新闻接口按时间窗返回全市场记录，采集器随后按公司名称筛选。若共用限频与重试后仍受限，本批省略新闻，记录近 30 天的新闻覆盖不完整，已独立取得的行情和资金数据仍可发布。新闻缺口不能解释为公司没有相关新闻。

## 配置与运行

1. 在部署的密钥管理或权限为 0600 的环境文件中配置 `TUSHARE_TOKEN`。API、worker、同步和管理进程使用同一数据库和同一配置。生产环境还需已有的 OIDC 与真实模型配置。变量名见 [部署模板](../deploy/.env.example)。
2. 设置 `VERA_DATA_PROVIDER=tushare`、`TUSHARE_SUBJECTS=600519.SH`（示例，可用逗号登记多只），配置 `TUSHARE_S3_ENDPOINT`、`TUSHARE_S3_REGION`、`TUSHARE_S3_ACCESS_KEY`、`TUSHARE_S3_SECRET_KEY` 与 `TUSHARE_S3_BUCKET`。使用独立测试数据库或已有真实数据环境。已有模拟实体与真实实体证券代码冲突时，发布会失败并保留原数据。
3. 确认许可覆盖本部署的存储、展示及模型处理用途后，设置 `TUSHARE_ALLOW_MODEL_PROCESSING=true` 与 `VERA_REAL_RESEARCH_ENABLED=true` 并重启相关进程。可在授权尚待确认时单独采集归档。模型用途默认关闭。
4. 应用宿主需能通过 TLS 连接 `api.tushare.pro:443` 和公告文件的登记主机。运行镜像已包含 `pdftotext` 和 `prlimit`。请求身分凭证只发往官方 HTTPS 数据接口。公告下载使用独立请求。原始响应、脱敏请求清单和 PDF 按 SHA-256 归档，失败的同步可重试。

在使用外置数据库的部署中执行以下命令。

```sh
vera admin provider test tushare
vera admin provider test tushare --research
vera sync
vera admin provider list
vera admin provider model-use tushare on
```

第一条只查配置中第一家公司的股票名录。同步会按标的采集及发布，并输出新增行数和缺数说明。一次同步覆盖标的、同行与对应披露。建议按账号配额安排收盘后的同步，`TUSHARE_MIN_INTERVAL` 的默认值为 500ms。同一数据库内的进程共享限频和冷却状态。部署或运维关闭用途后，后续同步不能自行恢复授权。使用 `vera admin provider model-use tushare off` 可在数据库即时撤销模型研究与相关展示。生产勿运行开发登录命令 `vera demo`。

后台同步与手动同步共用厂商级采集锁。两个任务研究不同股票时也可能读取同一指数或同行。每批从第一次取数到发布完成均由同一数据库会话持锁。等待中的任务可取消，持锁会话断开后该批发布失败，已有记录保留。

同步进程需要直连 PostgreSQL 或使用保留物理会话的连接池，以便在同一会话内发布。有效的空响应及其脱敏请求同样归档，供核对查询的股票和时段。

<a id="account-acceptance"></a>

## 用真实账号验收

从已有 Choice 测试站切换时，先在 Choice 配置下部署支持两家厂商的镜像并确认就绪，保存这次镜像 SHA、原有密钥配置版本和数据库备份。Compose 从宿主机 `/opt/vera/.env` 读取运行配置（权限 0600）。仅切到 TuShare 单厂商时需要设置 `VERA_REQUIRE_CHOICE_QUOTE=false`。组合模式可保留既有 Choice 行情就绪策略。对象存储可复用现有桶，需为 `TUSHARE_S3_*` 对应身份授予桶内 `tushare/` 前缀的写入与读取权限。重复归档会读回原文核对哈希。

在 `/opt/vera` 使用已验证的同一镜像运行 `VERA_IMAGE="$(cat .current_image)" docker compose up -d vera`，使容器加载新配置。回退时先恢复原 Choice 密钥配置版本，再用切换前记录的镜像重建容器并核对 `/readyz`。CD 自动回滚只恢复镜像，厂商配置由运维恢复。保留数据库和归档对象，以便读取历史报告及证据。

测试站的 Compose 默认运行 `vera demo`。启动后它会在后台同步，首次同步期间 `/readyz` 返回 `tushare_initial_sync`。每个登记标的已有归档日线后服务可就绪。若首次同步失败，可在运行中的测试容器重试。以下命令使用内嵌测试数据库的默认端口与开发登录配置。

```sh
docker compose exec -T vera sh -c 'export DATABASE_URL="${DATABASE_URL:-postgres://vera:vera-demo@127.0.0.1:${VERA_DEMO_PG_PORT:-55432}/vera?sslmode=disable}" VERA_DEV_OIDC=true; exec /app/backend/vera sync'
```

选择一家有近期有效日线、有效两融披露及申万二级分类的非金融 A 股公司；验收完整财务及同行章节时，还需有连续两个可比年报和足够同行。先查同步日志和厂商限制，再从统一工作台提交报告。记录技能版本与正式数据截止，核对复权曲线、最新两融日；若取得财报，再核对页码、同比计算输入和分部口径。组合部署同时打开 Choice 与 TuShare 的证据，核对厂商名、口径、日期、可用的归档定位和独立接收时间，并导出 PDF。缺少授权或覆盖时记录受限章节或无法执行原因；没有实际取得 Choice 观察的报告不能算双厂商验收。由模拟模型生成的报告带模拟标识，真实数据交付验收使用配置好的真实模型。

本轮范围是可追溯的公司收盘研究。TuShare 盘中报价、行业或全市场汇总、跨厂商自动择源和热备仍是后续工作。公募持仓全量汇总、互联互通季度持仓、机构观点、业绩说明会纪要还需接入可核验的数据。目标报告的对照基线见 [早期 MVP](../README-MVP.md)。
