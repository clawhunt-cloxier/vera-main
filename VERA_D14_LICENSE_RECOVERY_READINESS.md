# VERA_D14_LICENSE_RECOVERY_READINESS

模式：STRICT_READ_ONLY ｜ 日期：2026-11-18 ｜ 执行：C002
本轮未修改代码、未提交、未部署、未连接数据库、未读取或输出任何凭据。

---

## 0. 最终输出

```
G07_OWNER_AUTHORIZATION = NOT_PROVEN
G02_DATABASE_IDENTITY   = NOT_PROVEN   (ACCESS_LIMITED)
G02_LICENSE_RECORDS     = NOT_QUERIED  (ACCESS_LIMITED)
LICENSE_SCOPE_VERIFIED  = NOT_PROVEN   (仅代码层 PROVEN，见 §2.4)
D14_REPAIR_READY        = DESIGN_READY (设计就绪；实现未授权；真实授权依据未提供)
FINAL_STATUS            = INSUFFICIENT_EVIDENCE
```

`FINAL_STATUS` 不用 `READY_FOR_OWNER_REVIEW`，因为 G-02 两项都没有取得。设计部分可以评审，证据部分不能。

---

## 1. G-07：历史授权追溯 —— NOT_PROVEN

### 1.1 找到的记录（只列事实）

| 项 | 事实 | 来源 |
| --- | --- | --- |
| 提交 | `d521f56`，2026-11-16 23:48 +0800，作者 cloxier；提交说明末尾带 `Co-Authored-By: Claude Opus 5.5` | `git show` |
| 合并 | PR #36，分支 `agent/claude/T014-remove-license-gates`，2026-11-17 00:22:57 +0800，由 Cloxier 合并入 dev；合并标题：「remove source_licenses gate and Codes allowlist — allow any A-share research」 | `git log 0052ea8` |
| 提交自述的理由 | 「This made the system unusable for arbitrary A-share research」「SDK credentials authorize data access」 | 提交说明 |
| 提交到合并的间隔 | 34 分钟 | 时间戳 |
| 部署 | CD 于 2026-11-17 00:23 报告 `0052ea8` 部署成功 | PRE1.5 / 10 号文档 |

### 1.2 检查过但未找到授权的位置

| 检查位置 | 结果 |
| --- | --- |
| `TASKS.md` | 无任何任务行涉及移除许可闸。T014b 一行仍写「数据授权……保持受限」 |
| `docs/decisions/` ADR-0001…0005 | 无一条授权移除。ADR-0005 反而写「厂商账户及数据处理用途仍须按已确认授权和受控产品配置执行」 |
| `docs/handoffs/`（共 21 份，最新 11-15） | 无 11-16 之后的交接，无涉及移除的记录 |
| `docs/EMQUANT_INTEGRATION.md` | 写的是「引用字符串本身不证明厂商授权」「不从 SDK 登录成功推断授权」，与提交理由相反 |
| 远端分支 `agent/claude/T014-remove-license-gates` | 审计克隆中已不存在（AGENTS.md 规定合并后自动清理），**任务领取记录不可见** |
| PR #36 的描述与评论、审批记录 | **无法访问**：`gh` 令牌失效，GitHub API 此前被连接重置 |
| 本机 Claude Code 会话（102 个 jsonl） | 命中 `d521f56` 的只有两个，均为 11-17 之后的审计会话，不是创建该提交的会话。用「移除/删除/去掉 + 许可闸/白名单」扫描 Owner 消息，0 命中 |
| 本机 Codex 会话 | 11-16 当天无会话目录。11-17 的命中是后续的「独立审计」任务书，不是授权 |

### 1.3 结论

没有找到 Owner 明确授权删除 source-license 强制检查的证据，保持 **NOT_PROVEN**。

这个结论的边界：
- 它只说明**本机可访问的材料里没有**。PR #36 的评论与审批、创建该提交的会话、你与执行者的口头或其他渠道沟通，均不在可访问范围内。
- 它不等于「没有授权」，也不等于「未经授权」。不推断执行者动机。
- 合并标题里的「allow any A-share research」说明移除是**有意**的，但「有意」与「Owner 授权」是两件事，本轮不把前者当作后者。

**能让它变成 PROVEN 或 DISPROVEN 的材料**：PR #36 的描述/评论/审批页；创建 `agent/claude/T014-remove-license-gates` 的会话记录；你本人对「允许任意 A 股研究」的原始指示。

---

## 2. G-02：生产许可登记核验 —— ACCESS_LIMITED，已停止

### 2.1 停止原因

按你的规则「无法证明数据库身份或只读权限，立即停止」。具体：

| 需要证明的 | 现状 |
| --- | --- |
| 数据库身份（是 Vera 生产库，不是别的库） | **无法证明**。本机 `ssh` 配置中唯一的主机是 `touziagent.com`（finance-suite 站点）。没有证据表明它承载 Vera 的 PostgreSQL；Vera 在 `vera.touziagent.com`，经 Cloudflare，真实宿主未知（PRE1 / 10 号文档：线上版本为 CD 报告，未验容器） |
| 只读权限 | **无法证明**。本机无 `psql`、`docker`；无我可合法读取的连接串。获取连接串需读取服务器上的环境变量或密钥，这是被禁止的 |
| 连接方式 | 唯一可能路径是借用已有 ssh 私钥登录后再取库连接信息。这是**使用凭据**，且无法在连接前证明目标身份，所以没有尝试 |

补充：我曾准备发起一次 `ssh … hostname; whoami`，该命令因本机缺少 `timeout` 而**未执行**，没有产生任何连接。

### 2.2 因此没有取得的数据

`provider / dataset / method / field / instrument scope / 用途 / 有效期 / 撤销状态 / evidence_ref` —— **全部未查询**。我不能用交接文档里的「有效期至 2026-11-30T15:59:59Z」代替数据库事实；那是文档声明（PARTIAL），不是登记行。

### 2.3 解除阻塞所需（任选其一，由 Owner 提供）

1. **你本人在服务器上执行下面的只读查询并贴回结果**（推荐，不涉及我接触凭据）：
   ```sql
   SELECT current_database(), current_user, inet_server_addr(), pg_is_in_recovery();
   SHOW transaction_read_only;
   SELECT id, provider_id, product_code, dataset_id, vendor_method, field_names,
          instrument_scope, cardinality(instrument_codes) AS n_codes,
          allow_storage, allow_display, allow_model_processing,
          verified_by, verified_at, valid_from, valid_until, revoked_at,
          left(evidence_ref, 40) AS evidence_ref_prefix
   FROM source_licenses ORDER BY verified_at;
   SELECT id, name, kind, enabled, synthetic, allow_model_processing FROM providers;
   ```
   `evidence_ref` 只取前 40 字符，避免带出授权原文。
2. 你提供一个**明确标注为 Vera 生产库的只读角色**与连接方式，并确认我可以使用。
3. 提供 `docker logs vera` 中启动日志与 `/readyz` 的环境标识，用来先证明身份。

### 2.4 能确认的只有代码层（与数据库内容无关）

`RequireSourceUse`（`authorization.go:45-103`）一旦被调用，**校验是完整的**：同一事务内 `FOR SHARE` 锁定许可行与提供方行；用数据库时钟在加锁后重新判断；条件覆盖 provider、product、dataset、method、字段子集（`<@`）、证券范围（`all` 或代码子集）、`revoked_at IS NULL`、`verified_at/valid_from/valid_until`、提供方 `vendor AND enabled AND NOT synthetic`，以及按用途的 `allow_storage/display/model_processing`，模型用途还要求 `providers.allow_model_processing`。`provider.enabled` 只是其中一个合取条件，不能单独放行。

这是「机制正确」的证明，不是「登记内容正确」的证明。**LICENSE_SCOPE_VERIFIED = NOT_PROVEN**。

---

## 3. D14 最小修复设计（DESIGN_ONLY，不实现）

### 3.1 关键发现：被删的是校验，调用点还在

`d521f56` 之后，下列调用点**全部原样保留**，只是它们调用的函数内部变成了空操作：

| 调用点 | 位置 | 现状 |
| --- | --- | --- |
| 模型发送前预检 | `harness/observation_brief.go:70` `WithSnapshot(SourceModel)` | 调用在，内部校验空 |
| 每次发送（含重试）时复检 | `observation_brief.go:73-78` `WithSend` 内 `WithSnapshot(SourceModel)` | 同上 |
| 事实表构造 | `observation_brief.go:167` | 同上 |
| 存储/模型/展示三用途循环 | `observation_brief.go:42-43`、`216-217` → `Brief.Ready(use)` | `Ready` 只剩代码格式校验 |
| 展示接口 | `httpapi/observation_brief.go:48,76` `WithSnapshot(SourceDisplay)` | 同上 |
| 回放读取 | `brief.go` `Replay()` → `Ready` | `return out, nil` |
| 目录展示 | `WithDisplayCatalog` | 直接 `fn()` |

所以修复是**在少数几个函数内部恢复校验**，不需要改 harness、API 或前端的调用结构，也不影响 observation 协议。

### 3.2 唯一的结构性缺口：许可怎么找到

旧实现靠两个配置 UUID（`EMQUANT_SECURITY_LICENSE` / `EMQUANT_LIQUIDITY_LICENSE`）直接指向许可行，同时靠 `Codes` 白名单限定证券。要「保留任意 A 股输入」又恢复许可检查，必须把「配置 UUID」换成「按范围解析」：

- **新增一个解析函数**（放在 `dataservice` 包，与 `RequireSourceUse` 同层）：
  `ResolveSourceLicense(ctx, tx, provider, product, dataset, method, fields, code, use) (uuid.UUID, error)`
  在同一事务内按 `(provider, product, dataset, method)` 查未撤销、未过期、覆盖该字段与该代码、且对应用途为真的许可行，取唯一一行。0 行 → 拒绝。多行 → 取 `valid_until` 最晚者并记录，或拒绝（待 Owner 选，见 §5 决策 2）。
- 随后**仍调用原有的 `RequireSourceUse`**，传入解析出的 ID。这样**许可判断逻辑只有一份**（`RequireSourceUse`），解析函数只负责「找行」，不重复判断。这满足你之前 PRE2 任务书里「禁止在各入口复制不同版本的许可判断逻辑」。
- 不新增表、不改 `source_licenses` 结构、不改触发器。`instrument_scope='all'` 的许可行天然支持任意 A 股；`'listed'` 行则按列表限制。**是否任意 A 股可用，由登记的许可范围决定，而不是由代码白名单决定。**

### 3.3 改动清单（最小集）

| # | 文件 / 函数 | 改动 | 目的 |
| --- | --- | --- | --- |
| 1 | `dataservice/authorization.go` | 新增 `ResolveSourceLicense`（只读查询，不改既有函数） | 按范围找许可，替代配置 UUID |
| 2 | `emquant/brief.go` `Ready()` | 保留格式校验；对 `ref.security`（CSS）与 `mkt.daily_bar`（CSD）两个范围，解析并调用 `RequireSourceUse(use)` | 恢复用途级检查，覆盖存储/展示/模型三用途 |
| 3 | `emquant/brief.go` `Replay()` | 末尾恢复 `return out, b.Ready(ctx, tx, m.Code, use)`；manifest 校验恢复 `LicenseID` 字段比对 | 读取与回放时按**当前**许可重验，撤销后拒绝 |
| 4 | `emquant/brief.go` `WithDisplayCatalog()` | 恢复事务内持有展示许可；范围改为「所有已登记且未撤销的 display 许可」，不再遍历 `Codes` | 撤销立即影响目录与列表 |
| 5 | `emquant/brief.go` `Collect()` | 采集前对存储用途做一次解析与校验，把解析出的 `LicenseID` 传给 `RegisteredCollector` | 让 `storageGrant` 走有许可分支，原始对象带 `source_license_id` |
| 6 | `emquant/registered_collection.go` `storageGrant()` | 对 `LicenseID == Nil` **改为拒绝**（不再「跳过」）；`validateConfig()` 恢复 `LicenseID != Nil` | 无许可路径不再存在，fail-closed |
| 7 | `emquant/registry.go` `registerCube()` | `grant == nil \|\| LicenseID == Nil` 时**拒绝登记**，不再走「unlicensed path」 | 没有许可的原始对象不得入库 |
| 8 | `emquant/brief.go` `verifyArchive()` | SQL 恢复 `source_license_id` 条件 | 防止无许可对象被当作有许可对象回放 |
| 9 | `httpapi/handlers.go` 实时搜索 | 搜索调用的采集改为同样需要存储许可；拒绝时返回许可拒绝码，不是空下拉 | 搜索路径同样受控（`d521f56` 新增了它，也是无许可） |
| 10 | 错误分类 | 新增三类并保持可区分：`SOURCE_LICENSE_DENIED`（含子码）、`DATA_SOURCE_UNAVAILABLE`（厂商/网络）、`DATA_QUALITY_FAILED` | 不再把许可拒绝统一说成 `DATA_SOURCE_UNAVAILABLE`；顺带修复 D-01（错误被吞） |

**明确不做**：不恢复 `EMQUANT_BRIEF_CODES` 与 `Codes` 字段；不恢复两个 UUID 环境变量；不整体 revert `d521f56`；不新增厂商；不动 `harness` 调用结构；不动 observation 协议与前端；不改迁移。

### 3.4 满足你列出的五条要求

| 要求 | 如何满足 |
| --- | --- |
| 保留任意 A 股代码输入 | `briefCode` 正则保留；不恢复白名单。可用范围由许可行的 `instrument_scope` 决定 |
| 恢复数据集与用途级许可检查 | 改动 #2/#3/#4，沿用 `RequireSourceUse` 的数据集+方法+字段+用途条件 |
| 缺失/过期/撤销/范围不匹配时 fail-closed | `ResolveSourceLicense` 0 行即拒绝；`RequireSourceUse` 对过期/撤销/字段/代码/用途不匹配均返回无授权；改动 #6/#7 删除「无许可放行」分支 |
| 许可检查在模型消费数据之前 | 既有顺序保留：采集前（#5）→ 冻结前 → 模型预检 → 每次发送内复检（`WithSend`）。因为只是恢复函数内部校验，**该顺序不需要新建，只需要让它重新生效** |
| 不整体 revert / 不新增信源 / 不部署 | 见 §3.3「明确不做」 |

### 3.5 风险与注意

- **fail-closed 的直接后果**：修复一旦上线，若 `source_licenses` 里没有覆盖范围的有效行，**所有看票请求都会被拒绝**。这是正确的失败方式，但上线前必须先确认登记行存在（G-02）。
- **许可到期**：交接文档称许可 `2026-11-30T15:59:59Z` 到期。修复后到期即全部阻断。续期必须基于真实授权依据，不得自动重授（`00012` 的 `evidence_ref` 与 `verified_by` 是人工字段）。
- **历史数据**：`d521f56` 之后产生的原始对象 `source_license_id` 为空（`raw_object_license_scope` 约束允许全空）。修复后这批对象**无法通过** `verifyArchive` 的许可条件，会被拒绝回放。这是期望行为，但意味着 11-17 00:23 之后生成的任何报告将不可再展示。是否需要处理由 Owner 决定（见 §5 决策 3）。
- **本设计依据的是代码，不是库内容**：若生产库 `source_licenses` 实际为空或范围不同，设计仍成立，但「修复后系统能否出报告」未知。

### 3.6 负向测试清单（设计，不实现）

沿用 PRE2 任务书的十项，对应阻断入口：

| 场景 | 预期阻断入口 | 回执 |
| --- | --- | --- |
| Missing License | `Collect`（#5） | `SOURCE_LICENSE_DENIED/NO_GRANT` |
| Expired License | `Ready`（#2） | `…/EXPIRED` |
| Revoked License | `Ready`、`WithDisplayCatalog` | `…/REVOKED` |
| Wrong Dataset | `ResolveSourceLicense` | `…/NO_GRANT` |
| Wrong Field | `RequireSourceUse`（`<@` 不满足） | `…/FIELD_OUT_OF_SCOPE` |
| Wrong Instrument | `RequireSourceUse`（`'listed'` 不含该代码） | `…/INSTRUMENT_OUT_OF_SCOPE` |
| Wrong Use | `RequireSourceUse`（用途位为假） | `…/USE_NOT_GRANTED` |
| License Service Unavailable（库不可达） | `Ready` | `DATA_SOURCE_UNAVAILABLE`，且**不放行** |
| Revoked After Snapshot | 模型 `WithSend` 内复检 | 拒绝发送，预算释放 |
| Replay After Revocation | `Replay`（#3）、`verifyArchive`（#8） | 拒绝回放 |

注意：现有 `brief_test.go` 在 `d521f56` 中**删除了撤销测试并改为断言 2xx**，修复时这些断言要反向恢复，否则测试会和修复冲突。

---

## 4. 证据缺口（本轮后仍开放）

| # | 缺口 | 影响 |
| --- | --- | --- |
| G-07 | 授权证据无法在本机找到；PR #36 页面不可达 | 决定 D14 的性质是「已授权的取舍」还是「需纠正的偏离」 |
| G-02a | 数据库身份未证明 | 无法确认任何后续查询指向正确的库 |
| G-02b | `source_licenses` 实际内容未知 | 无法判断修复后能否立即工作，也无法确认到期日 |
| G-10 | 11-17 00:23 之后是否已有请求在无许可路径上产生过数据 | 决定 §3.5 历史对象的处置范围；需要线上日志或库查询 |
| G-11 | PRE2（D14 修复设计）任务书存在于 `3364279f` 会话，但磁盘上**未找到** `D14_*.md` 六份交付物 | 本文是其子集（最小修复），不是它的替代 |

---

## 5. 需要 Owner 决定

1. **G-07 的性质**：你是否在任何渠道授权过删除许可闸？回答「是」则 D14 记为「已授权的取舍，现撤销授权」；回答「否」则记为「偏离」。两种都不影响修复设计，但影响事后记录与责任归属。
2. **多行许可冲突时的策略**：同一范围出现多条有效许可，是「取最晚到期」还是「拒绝并报警」？我倾向**拒绝**（更保守，不会因登记重复而悄悄放行）。
3. **`d521f56` 之后产生的无许可原始对象**：保留但禁止展示和回放（默认行为），还是需要单独的清理/重采集任务？
4. **G-02 的取证方式**：选 §2.3 的 1、2、3 之一。
5. **真实授权依据**：恢复校验后，是否有可填入 `evidence_ref` 的真实依据？没有的话，修复上线即等于关停看票功能。

---

## 6. 本轮未做

未连接任何数据库或服务器；未读取 `.env`、密钥或连接串；未调用任何厂商接口；未修改任何文件（仅新增本文档）；未运行测试；未提交、未部署。

**等待 Owner 审批。**
