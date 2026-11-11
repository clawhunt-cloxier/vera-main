# Vera 部署指南

## 服务器环境准备

### 1. 创建部署用户和配置 Docker

已在服务器 `38.76.222.210` 上完成：
- 安装 Docker 和 docker-compose-v2
- 创建 `deploy` 用户并加入 docker 组
- 配置 sudoers 允许 deploy 用户管理 vera 服务
- 生成 SSH 密钥对用于 CD

### 2. 在服务器上创建部署目录

```bash
ssh deploy@38.76.222.210
sudo mkdir -p /opt/vera
sudo chown deploy:deploy /opt/vera
cd /opt/vera
```

### 3. 创建 `.env` 文件

```bash
cd /opt/vera
cp deploy/.env.example .env
# 编辑 .env 填入真实的凭证
```

必填环境变量：
- `VERA_DB_DSN`: PostgreSQL 连接串（推荐使用托管数据库）
- `VERA_SESSION_SECRET`: 会话密钥（用 `openssl rand -base64 32` 生成）
- `VERA_OIDC_CLIENT_ID` / `VERA_OIDC_CLIENT_SECRET`: OIDC 登录凭证
- `VERA_MODEL_API_KEY`: 模型 API 密钥

### 4. 配置 GitHub Secrets

在 GitHub 仓库的 Settings > Secrets and variables > Actions 中添加：

- `DEPLOY_HOST`: `38.76.222.210`
- `DEPLOY_SSH_KEY`: 服务器上 deploy 用户的私钥（已生成并打印）
- `GHCR_PAT`: GitHub Personal Access Token（需要 `packages:read` 权限）

## CD 工作流

工作流在 `.github/workflows/deploy.yml`，触发条件：
- CI 工作流在 `dev` 分支成功完成后自动触发
- 构建 Docker 镜像并推送到 GHCR
- SSH 到服务器，拉取新镜像并重启服务

## 手动部署

```bash
ssh deploy@38.76.222.210
cd /opt/vera

# 拉取最新镜像
docker compose pull

# 启动服务
docker compose up -d

# 查看状态
docker compose ps
docker compose logs -f vera-api
docker compose logs -f vera-worker

# 健康检查
curl http://localhost:8080/readyz
```

## 数据库迁移

首次部署需要运行迁移：

```bash
docker compose run --rm vera-api migrate
```

## 服务管理

```bash
# 重启
docker compose restart

# 停止
docker compose down

# 查看日志
docker compose logs -f

# 进入容器
docker compose exec vera-api sh
```

## Nginx（可选）

如果需要 HTTPS 和域名访问：

1. 将 SSL 证书放到 `deploy/ssl/` 目录
2. 取消注释 `deploy/nginx.conf` 中的 HTTPS server 块
3. 重启 nginx 容器

## 监控和日志

- 健康检查端点：`/readyz`
- API 日志：`docker compose logs vera-api`
- Worker 日志：`docker compose logs vera-worker`
- 数据持久化：Docker volume `vera-data`

## 回滚

```bash
# 查看历史镜像
docker images ghcr.io/clawhunt-cloxier/vera-main

# 切换到特定版本
docker tag ghcr.io/clawhunt-cloxier/vera-main:<sha> ghcr.io/clawhunt-cloxier/vera-main:dev
docker compose up -d
```
