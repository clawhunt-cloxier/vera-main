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
| `.current_image` | 最近一次健康的镜像标签，CD 用于回滚 |

`.env` 中的模型密钥、价格表版本等使用真实变量名（`MODEL_{WRITER|VERIFIER|ROUTER}_*`、`VERA_PUBLIC_URL` 等），完整列表见 `backend/internal/platform/config/config.go`。

## GitHub Secrets

在仓库 Settings → Secrets and variables → Actions 配置：

- `DEPLOY_HOST`：服务器地址。
- `DEPLOY_SSH_KEY`：deploy 用户的 SSH 私钥。
- `GHCR_PAT`：用于服务器拉取镜像的 PAT，仅需 `read:packages`。
- `DEPLOY_KNOWN_HOSTS`（建议）：服务器主机指纹（`ssh-keyscan` 输出）。未设置时 CD 回退为现场扫描，存在被中间人替换的风险。

## 更新与回滚

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
