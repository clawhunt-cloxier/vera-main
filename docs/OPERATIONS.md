# 运行环境与运维

状态：MVP 规格（ADR-0003 D11）。原则：能本地自建的服务全部自建，外部依赖只保留数据厂商 API 与模型 API。

## 1. 组件

| 组件 | 用途 | 部署 | 版本 |
| --- | --- | --- | --- |
| PostgreSQL 18 | 业务库、River、统一数据集 | 容器，持久卷 | T012 锁定镜像摘要 |
| S3 兼容对象存储 | 原始响应、长文本、报告产物 | 容器（MinIO 或许可明确的替代品），持久卷 | T012 锁定，并记录许可 |
| Keycloak | OIDC 身份源：本地账号、密码策略、会话 | 容器，使用业务 PostgreSQL 的独立数据库 | T012 锁定 |
| Caddy | TLS、静态文件、反向代理 `/api` 与 `/auth` | 容器 | T012 锁定 |
| vera-api、vera-worker | 应用 | 同一镜像，不同命令 | 构建产出 |

后端只依赖 S3 API，不使用某一实现的私有接口；对象存储实现可替换。若所选镜像的许可或分发方式不适合，T012 改用其他 S3 兼容实现并在交接中记录。

## 2. 环境

| 环境 | 用途 | 数据 | 模型 | 身份 |
| --- | --- | --- | --- | --- |
| local | 开发 | fixture（demo-universe）| replay 或真实 key | Keycloak 开发 realm，预置测试用户 |
| test | 给产品与客户看效果（DEMO 阶段）| fixture，厂商资料到位后切换真实厂商 | 真实 key | Keycloak test realm，邀请码注册 |
| pilot | 一线投研人员试用 | 真实厂商 | 真实 key | 同 test |

当前已上线的 test 环境是 MVP 形态：单个容器运行 `vera demo`（内嵌 PostgreSQL、开发登录、模拟数据），宿主机 nginx 终止 TLS（自签证书），由 dev 分支 CD 自动更新，见 [DEPLOYMENT](DEPLOYMENT.md)。下面的 Compose/Keycloak/Caddy 形态是 T012/T023 的目标，尚未实现。

`make dev-up` 启动 local 全部组件；`deploy/compose.test.yml` 用于 test 机器。配置来自 `.env`（不入库）与 `deploy/config/*.yaml`（入库，无密钥）。

test 环境在拿到真实厂商资料前可以用 fixture 运行，此时页面顶部固定显示“演示数据”横幅，报告标注 `synthetic=true`。production 模式（pilot）拒绝 fixture 与 replay 适配器。

## 3. 身份

- Keycloak realm 配置导出为 `deploy/keycloak/realm-vera.json` 入库（不含密钥）；客户端为机密客户端，回调地址按环境配置。
- 允许自助注册，但 Vera 首次登录必须兑换邀请码才创建 workspace；未兑换的账号只能看到 onboarding 页。
- test 环境不配置邮件服务：关闭邮箱验证；忘记密码由管理员通过 CLI 重置。pilot 前配置 SMTP（EXT-03）。

## 4. 管理 CLI（`vera-admin`）

首版没有管理后台网页，管理操作用 CLI，所有操作写审计事件。

| 命令 | 作用 |
| --- | --- |
| `invite create --count N --expires 14d` | 生成邀请码（只显示一次，库中存哈希）|
| `invite list` / `invite revoke <id>` | 查看、作废 |
| `user list` / `user disable <id>` / `user reset-password <id>` | 用户管理（后两项调用 Keycloak Admin API）|
| `quota set --user <id> ...` | 单用户配额覆盖 |
| `provider list` / `provider test <id>` | 厂商配置与连通性测试（只读请求）|
| `data sync --dataset <id> --since <date>` | 手动触发数据刷新 |
| `usage report --day <date>` | 当日模型调用、token、费用，按用户与技能汇总 |
| `usage reconcile` | 列出 UNKNOWN 调用，按厂商账单手工确认后核销 |
| `kill-switch on|off` | 停止接受新研究任务（在途任务继续）|
| `task inspect <id>` | 打印步骤台账、模型调用、被删除结论（等同 `vera-harness replay --dry-run`）|

## 5. 运行要求

- 观测：结构化 JSON 日志（字段见 [CONVENTIONS](CONVENTIONS.md) §5）；`/readyz`；每日 `usage report` 输出到日志。MVP 不部署 Prometheus/Grafana，T023 视需要增加。
- 告警：当日费用达到总上限 80%、River 作业失败率异常、厂商连续认证失败，写 ERROR 日志并在 `vera-admin usage report` 中显示；邮件告警在 pilot 前配置。
- 备份：PostgreSQL 每日 `pg_dump` 到对象存储的独立 bucket，保留 14 天；对象存储每周同步到 test 机器外的位置（EXT-03 提供）。T023 做一次恢复演练并记录用时。
- test 机器最低配置建议：4 vCPU、16 GB 内存、200 GB SSD、可访问厂商与模型 API 的出站网络。

## 6. 发布到 test 的步骤

1. 在 dev 上选定通过 Verify 的 commit，构建镜像并记录摘要。
2. test 机器执行备份。
3. 执行迁移（只做兼容迁移）。
4. 依次更新 worker、api、web。
5. 冒烟：登录、生成计划、跑一个 fixture 个股分析、打开报告。
6. 失败则回退到上一镜像摘要；不执行破坏性回退迁移。
