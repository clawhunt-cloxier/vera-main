# OPERATIONS §1: 依赖版本锁定

本文档记录生产环境使用的依赖版本，确保可重现的构建和部署。

## 运行时依赖

| 组件 | 版本 | 用途 |
|------|------|------|
| Go | 1.23.3 | 后端运行时 |
| Node.js | 22.11.0 | 前端构建与运行 |
| PostgreSQL | 17 | 主数据库 |
| Keycloak | 26.0.7 | 身份认证 |
| Caddy | 2.8.4 | 反向代理 |
| MinIO | RELEASE.2024-11-07T00-52-20Z | 对象存储 |

## Go 依赖

主要后端依赖（见 `backend/go.mod` 完整列表）：

| 包 | 版本 | 用途 |
|---|------|------|
| github.com/jackc/pgx/v5 | v5.7.1 | PostgreSQL 驱动 |
| github.com/riverqueue/river | v0.51.1 | 后台作业队列 |
| github.com/pressly/goose/v3 | v3.23.1 | 数据库迁移 |
| github.com/google/uuid | v1.6.0 | UUID 生成 |
| github.com/shopspring/decimal | v1.4.0 | 精确小数计算 |

## 前端依赖

主要前端依赖（见 `web/package.json` 完整列表）：

| 包 | 版本 | 用途 |
|---|------|------|
| react | ^18.3.1 | UI 框架 |
| typescript | ~5.6.2 | 类型系统 |
| vite | ^5.4.8 | 构建工具 |
| @tanstack/react-query | ^5.59.0 | 数据获取 |
| tailwindcss | ^3.4.14 | CSS 框架 |

## 开发工具

| 工具 | 版本 | 用途 |
|------|------|------|
| golangci-lint | 2.15.0 | Go 代码检查 |
| ESLint | ^9.13.0 | JavaScript/TypeScript 检查 |
| Prettier | ^3.3.3 | 代码格式化 |
| sqlc | 1.27.0 | SQL 代码生成 |

## Docker 镜像

生产环境使用的基础镜像：

```
postgres:17-alpine
quay.io/keycloak/keycloak:26.0.7
caddy:2.8.4-alpine
quay.io/minio/minio:RELEASE.2024-11-07T00-52-20Z
golang:1.23.3-alpine (构建)
node:22.11.0-alpine (构建)
```

## CI/CD

| 工具 | 版本 | 配置文件 |
|------|------|----------|
| GitHub Actions | N/A | .github/workflows/*.yml |
| actions/setup-go | v5 | Node 24 runtime |
| actions/setup-node | v4 | Node 24 runtime |

## Python 工具链

开发工具依赖（见 `tooling/requirements.txt`）：

```
PyYAML==6.0.2
jsonschema==4.23.0
```

## 更新策略

- **运行时依赖**: 只在经过测试后更新 minor/patch 版本
- **Go 依赖**: 使用 `go get -u` 更新，运行完整测试套件后合并
- **前端依赖**: 使用 `npm update` 更新，检查构建和类型检查通过
- **开发工具**: 独立更新，不影响生产构建

## 安全更新

安全补丁应优先应用：
1. 检查 GitHub Security Advisories
2. 运行 `go list -json -m all | nancy sleuth`（Go）
3. 运行 `npm audit`（前端）
4. 更新受影响的依赖
5. 运行完整测试套件
6. 快速合并并部署

## 验证

使用以下命令验证版本：

```bash
# 检查工具链版本
make check-toolchain

# 检查 Go 依赖
cd backend && go list -m all

# 检查前端依赖
cd web && npm list --depth=0

# 验证 Docker 镜像
docker compose config | grep image:
```
