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

`VERA_DATA_PROVIDER=emquant` 接入真实 SDK 和受控证券范围，使用独立 `observation.v1` 协议。仍是测试环境开发登录；报告只覆盖已核实的日成交观察，非实时，完整范围见 [简报技能](skills/stock-observation-brief.md)。

1. 独立部署持久 S3，测试配置见 `deploy/storage.compose.yml`（SeaweedFS 4.00，镜像固定摘要）。配置身份 JSON 放 `/opt/vera/storage/s3.json`，宿主父目录仅 deploy 可访问，容器只读挂载；凭证不入 Git。创建 `vera-objects` volume、`vera-archive` network 和 `vera-raw` bucket。S3 只绑定宿主 loopback，应用经私有 Docker network 访问。原 MinIO 固定下载已不可用。
2. 持久保存 SDK 及运行配置于 `/opt/vera/sdk`，容器挂载 `/opt/choice-sdk`，属主 uid/gid 10001；保留设备标识，不同时运行两个使用该 Choice 账号的采集进程。
3. 准备服务器秘密配置，变量见 `.env.example`。使用已核实的产品、字段、证券和用途依据显式运行 `vera emquant init-brief`；它拒绝改写旧许可或恢复撤销。不要依据 SDK 调用成功推断实时权利。
4. 先经过 dev Verify/CD 发布支持新版本的镜像，再停旧 worker、保留 `.env` 和镜像回滚副本，切换 `VERA_DATA_PROVIDER=emquant` 并重建容器。库加载、归档、许可、实体同步任一步失败应停服排查或回退兼容真实模式的镜像，不展示模拟回退。同库存在真实证券身份时 fixture 启动会拒绝；切换前应保留独立数据库备份。来源许可和真实对象留存不随应用回滚删除。
5. 从网站完成登录、真实证券选择、计划确认、进度、报告和证据读取；记录报告 ID、镜像 SHA 和实测时间，不能以健康接口替代验收。真实 `TIME`、延迟/交易状态、财报版本仍须后续独立核实。
