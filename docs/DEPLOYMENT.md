# Vera 测试环境部署

测试环境对应 `dev` 分支：PR 合入 dev 且 `Verify` 工作流通过后，`Deploy to Test Environment` 自动构建镜像、推送 GHCR，并通过 SSH 更新服务器。

## 架构

- 单个容器运行 `vera demo`：内嵌 PostgreSQL、开发登录、API、Worker 与前端静态文件。
- 数据持久化在 Docker volume `vera-data`（挂载到容器 `/data`）。
- 宿主机 nginx 终止 TLS，反向代理到 `127.0.0.1:8080`，站点配置模板见 `deploy/nginx-host.conf`。
- 镜像：`ghcr.io/<owner>/<repo>`，标签 `dev` 与 `sha-<12位commit>`。

## 服务器目录 `/opt/vera`（属主 deploy）

| 文件 | 说明 |
| --- | --- |
| `compose.yml` | 由 CD 每次上传，来源 `deploy/compose.yml` |
| `.env` | 权限 600，只在服务器上维护，不入库，模板见 `deploy/.env.example` |
| `remote-deploy.sh` | 由 CD 每次上传，来源 `deploy/remote-deploy.sh`；负责登录 GHCR、拉取、启动、健康检查与回滚，ssh 的 stdin 只传 GHCR 令牌 |
| `.current_image` | 最近一次健康的镜像标签，CD 用于回滚 |

`.env` 中的模型密钥、价格表版本等使用真实变量名（`MODEL_{WRITER|VERIFIER|ROUTER}_*`、`VERA_PUBLIC_URL` 等），完整列表见 `backend/internal/platform/config/config.go`。

## GitHub Secrets

在仓库 Settings → Secrets and variables → Actions 配置：

- `DEPLOY_HOST`：服务器地址。
- `DEPLOY_SSH_KEY`：deploy 用户的 SSH 私钥。
- `GHCR_PAT`：用于服务器拉取镜像的 PAT，仅需 `read:packages`。
- `DEPLOY_KNOWN_HOSTS`（建议）：服务器主机指纹（`ssh-keyscan` 输出）。未设置时 CD 回退为现场扫描，存在被中间人替换的风险。

## 更新与回滚

- 触发：push 到 dev，同一 commit 的 `Verify` 成功后才构建；只改 `**.md` 或 `docs/**` 的推送不会触发部署，`deploy/**` 的改动会触发。

- 自动：CD 拉取 `sha-<commit>` 镜像并启动，轮询 `/readyz` 最多约 200 秒；失败时自动回滚到 `.current_image` 记录的上一版本，并让工作流失败。
- 手动回滚：

```bash
cd /opt/vera
VERA_IMAGE=ghcr.io/<owner>/<repo>:sha-<旧commit> docker compose up -d
echo ghcr.io/<owner>/<repo>:sha-<旧commit> > .current_image
```

## 常用命令

```bash
docker compose ps
docker compose logs -f vera
curl -fsS http://127.0.0.1:8080/readyz
```

## 注意事项

- `vera demo` 使用开发登录，任何知道地址的人都能以任意用户名登录并消耗模型额度。测试环境对公网开放前，应加访问保护（nginx basic auth 或 IP 白名单），并保持 `VERA_DAILY_MODEL_COST_LIMIT_USD` 在可接受范围。
- 内嵌 PostgreSQL 不能以 root 运行，镜像已使用非 root 用户 `vera`（uid 10001）；volume 首次创建时会继承 `/data` 的属主。
- 轮换：对话中出现过的 DeepSeek 密钥、root 密码、deploy 私钥建议在上线后轮换。

## 真实成交观察简报切换

`VERA_DATA_PROVIDER=emquant` 接入 Choice SDK。旧 `observation.v1` 仍是兼容路径；它的历史验收记录只覆盖当时的测试标的，不代表当前 0.4 的标的范围。

1. 独立部署持久 S3，测试配置见 `deploy/storage.compose.yml`（SeaweedFS 4.00，镜像固定摘要）。配置身份 JSON 放 `/opt/vera/storage/s3.json`，宿主父目录仅 deploy 可访问，容器只读挂载；凭证不入 Git。创建 `vera-objects` volume、`vera-archive` network 和 `vera-raw` bucket。以独立项目运行 `docker compose -p vera-archive -f storage.compose.yml up -d`，不可与应用共享默认 Compose 项目，否则应用部署的 `--remove-orphans` 会移除存储容器。现有 docker-run 存储实例须先在维护窗口按同一 volume 迁移，不能同时创建同名容器。S3 只绑定宿主 loopback，应用经私有 Docker network 访问。原 MinIO 固定下载已不可用。
2. 持久保存 SDK 及运行配置于 `/opt/vera/sdk`，容器挂载 `/opt/choice-sdk`，属主 uid/gid 10001；保留设备标识，不同时运行两个使用该 Choice 账号的采集进程。
3. 准备服务器秘密配置，变量见 `.env.example`。使用已核实的产品、字段、证券和用途依据显式运行 `vera emquant init-brief`；它拒绝改写旧许可或恢复撤销。不要依据 SDK 调用成功推断实时权利。
4. 先经过 dev Verify/CD 发布支持新版本的镜像，再停旧 worker、保留 `.env` 和镜像回滚副本，切换 `VERA_DATA_PROVIDER=emquant` 并重建容器。库加载、归档、许可、实体同步任一步失败应停服排查或回退兼容真实模式的镜像，不展示模拟回退。同库存在真实证券身份时 fixture 启动会拒绝；切换前应保留独立数据库备份。来源许可和真实对象留存不随应用回滚删除。
5. 从网站完成登录、真实证券选择、计划确认、进度、报告和证据读取；记录报告 ID、镜像 SHA 和实测时间，不能以健康接口替代验收。真实 `TIME`、延迟/交易状态、财报版本仍须后续独立核实。

2026-11-16 的旧 observation 验收使用东方财富 300059.SZ；该记录仅证明历史兼容路径，不限制当前 0.4 的 Choice 标的范围。

## T027 单股行情简报 0.4

部署前执行新增迁移 00019；API 与 worker 都必须支持 `1.1.0-demo.3`。先部署新版镜像并确认旧 observation 模式仍可启动；然后在正在运行的 `vera` 容器内使用一次性环境变量执行 `vera emquant init-quote`，让同一数据库登记或复用一个覆盖全部 A 股的 `source_licenses` 与 `quote_mapping_policies` 行。治理身份由数据库生成并留在治理层，应用和部署配置不保存逐标的实例。不要用 `docker compose run` 执行初始化，它会启动第二个内嵌 PostgreSQL 实例。未登记有效 all-scope Choice 行情治理行时继续提供 observation.v1；Choice SDK 登录成功不能替代授权、字段时区、单位、延迟、新鲜度阈值和状态映射核验。

0.4 前端默认选择 `latest_available`，用户也可以显式改为 `realtime_trade`。选择实时成交时要求连续交易状态、已确认实时性质和满足阈值的事件时间；选择最近可用时保留实际延迟和交易状态，仍受登记的新鲜度阈值限制。采集失败、权限缺失或过期不会回退为 observation 或 fixture。回滚可关闭新计划入口，但须保留迁移和已经冻结的清单，并让支持该版本的 worker 处理在途任务。

实际发布与真实盘中验收状态以 [T027 交接](handoffs/2026-11-17-T027-codex.md) 为准。开发测试通过不能替代真实账号、映射依据和交易时段端到端验收。

实时行情的许可和映射使用 `vera emquant init-quote` 登记。该命令登记覆盖全部 A 股的 Choice 数据治理记录，要求字段、时间/价格口径、延迟性质、新鲜度阈值和证据引用；记录由数据库生成并受审计保护。`EMQUANT_QUOTE_LATENCY_MODE=real_time` 还必须有经核实的状态字段，并至少映射一个 `continuous` 状态；无法证明时使用 `unknown`，前端只能选择 `latest_available`。命令只登记治理事实，不向 Choice 发起请求，也不授予不存在的厂商权限。

当前 MVP 先登记 `unknown` 延迟策略并运行 `latest_available`。每个请求的证券代码直接交给 Choice；Choice 返回失败、空结果或无法通过字段/时间/新鲜度校验时，任务明确报错或弃权。交易时段核对 Choice 终端的最新价、`TIME`、接收时间和连续交易状态后，若证据充分，再登记 `real_time`；旧策略不原地修改。

登记完成后设置 `VERA_REQUIRE_CHOICE_QUOTE=true` 并重启；此时 `/readyz` 会检查 Choice provider、all-scope source license 和 mapping policy 是否存在、未撤销、未过期且允许存储/展示/模型处理，缺失时返回 `realtime_source_unavailable`。登记前保持默认 false，避免新镜像在治理行写入前被部署健康检查回滚。

首次启用后用受控账号运行 `npm run accept:live-latest` 验收 `unknown`/`latest_available`；策略升级后使用 `npm run accept:live-realtime` 验收严格实时。两条路径都核对 0.4 计划、采集、冻结、报告、证据及时间；在受控验收记录中关联报告 ID 和镜像 SHA。运行条件见 [TESTING](../TESTING.md)。
