# Logto 1.46.0 管理控制台子路径

运营人员从 UniHub 的 `/auth` 进入 Logto，以已有管理租户账号登录。Console 使用原生授权码流程和 S256 PKCE，回调为 `https://unihub.quukk.com/auth/console/callback`。管理 API 按登录管理员的用户令牌和权限执行读写。精确路径 `/auth/callback` 由 UniHub 接收。

## 构建和交付

源版本为 `v1.46.0`，提交为 `d179bf9d2d51104236a342b90c2841df66e4ba5b`。113 上 `logto-oss-app-1` 对应的基础镜像摘要为 `sha256:d5e208ce705cbcf7c79ddba421216eb65413d0b0531aa658fb22a4ba659be983`。

将 LOGTO_SOURCE 指向含该标签的本地 Logto 仓库，在本目录执行以下命令。构建使用 Git、Node、npm、Python 3、rsync 和平台编译工具。实测 Node 26.13.1。脚本创建临时检出，核对提交后应用 `logto-1.46.0.patch`，安装 pnpm 10.28.2，按冻结的锁文件构建，再由 `pack-overlay.py` 打包。构建只需源码和依赖，部署环境变量在容器启动时提供。

```sh
out=$(mktemp -d)
./build-overlay.sh "$LOGTO_SOURCE" "$out"
(cd "$out" && shasum -a 256 -c SHA256SUMS)
cmp SHA256SUMS "$out/SHA256SUMS"
```

交付文件为 `logto-1.46.0-admin-auth-overlay.tar.gz`，共 21,740,735 字节。两次独立干净重建的压缩包逐字节一致。SHA-256 为 `a67cd3f13efeb9ed8f32720f19f7afefbcf49affdc7a923a142e00fd213426e0`。归档由本地交付或从补丁重建，Git 保存补丁、构建脚本和校验清单。

| 归档内目录 | 运行镜像目标（可只读挂载） |
| --- | --- |
| `packages/core/build` | `/etc/logto/packages/core/build` |
| `packages/console/dist` | `/etc/logto/packages/console/dist` |
| `packages/experience/dist-admin` | `/etc/logto/packages/experience/dist-admin` |
| `packages/account/dist-admin` | `/etc/logto/packages/account/dist-admin` |

独立镜像可以直接使用相邻的 `../logto.Dockerfile`。它完整替换 Core 和 Console 输出，将管理员专用的 Experience、Account Center 与原版 `dist` 并存。若采用目录挂载，先将归档解压到新的空目录，再按表中四项逐个挂载。

## 部署与回滚

由父项目在 Logto 主机执行。进入含已交付压缩包和 `SHA256SUMS` 的本目录，基础镜像使用上面的固定摘要。

```sh
sha256sum -c SHA256SUMS
export LOGTO_PORTAL_IMAGE="unihub-local/logto-console:1.46.0-$(cut -c1-12 SHA256SUMS)"
docker build --network none -f ../logto.Dockerfile -t "$LOGTO_PORTAL_IMAGE" .
docker compose --env-file /opt/logto/.env -f ../logto-sidecar.yml up -d portal
```

启动工作目录为 `/etc/logto`，运行命令为 `npm start`。沿用原数据库、加密设置及 Docker 网络。独立 Compose 项目只发布 `127.0.0.1:13003:13003` 和 `127.0.0.1:13004:13004`。

| 运行变量 | 值 |
| --- | --- |
| `ADMIN_CONSOLE_BASE_PATH` | `/auth` |
| `ADMIN_ENDPOINT` | `https://unihub.quukk.com/auth/` |
| `ADMIN_PORT` | `13003` |
| `PORT` | `13004` |
| `ENDPOINT` | `https://auth.quukk.com` |
| `TRUST_PROXY_HEADER` | `1` |

管理监听校验传输 Host、外部 Host 和 HTTPS。**Compose 的 Node 健康检查应直接请求** `http://localhost:13003/auth/oidc/.well-known/openid-configuration`，并传 `X-Forwarded-Host: unihub.quukk.com`、`X-Forwarded-Proto: https`。在本地 Node 26 实测，请求 `127.0.0.1` 同时手设 `Host: localhost:13003` 返回 404，使用 `localhost` URL 返回 200。生产预期 discovery issuer 为 `https://unihub.quukk.com/auth/oidc`。

在 Nginx 的 `http` 中包含 `logto/cookies.http.conf`，在 UniHub TLS `server` 中包含父级 `common.conf` 和 `logto/locations.conf`。将两个 `.inc` 放在相对 Nginx 主配置目录的 `logto/` 中，运行 `nginx -t` 后加载。每个私有 location 使用 `auth_request /_unihub_operator;` 和 `error_page 401 = @unihub_portal_login;`。上游 401 由原生客户端接收。13003 接收精确白名单内的 Logto Cookie 及原生请求头。13004 的管理 API 接收当前用户的 bearer，Cookie 为空。浏览器用原生 SDK 获取用户令牌，服务器端 M2M 密钥无需参与。防嵌套响应头按上游原样传递。

共享 DB 所需的 schema、客户端配置、全局登录配置变更均为零。精确回调和登出 URI 由独立进程为内置客户端生成。正常登录会写入管理租户会话、授权与审计数据，管理员主动保存的业务修改按原有语义持久化。新管理 issuer 的 Cookie 签名和不透明授权数据按 issuer 隔离。回滚时先撤下新入口和对应两处 Nginx include，保留父级 UniHub 回调，随后运行下列命令。回滚不需要还原共享数据库。

```sh
docker compose --env-file /opt/logto/.env -f ../logto-sidecar.yml stop portal
```

发布或回滚前后，对比 `https://auth.quukk.com/oidc/.well-known/openid-configuration`，并从原入口用原管理员打开 `http://localhost:13002/console/users`。原 13001/13002 继续由原实例提供服务。

## 已完成的本地验收

本地 PostgreSQL 使用临时数据，原版和独立实例共用同一测试管理员。Firefox 只在临时配置中信任测试 CA。Nginx 模拟父项目的操作员子请求和登录恢复。以下结果来自运行交付归档的真实 Logto 进程。

| 场景 | 实测结果 |
| --- | --- |
| 管理员登录和用户管理 | 原生登录、S256 校验和回调 state 匹配。进入 `/auth/console/users`，修改测试用户，刷新读回并恢复原值。 |
| 账号中心 | 从头像菜单打开 `/auth/account/profile`，完成账号中心的原生登录，请求 `/auth/api/my-account` 返回 200。 |
| 原实例兼容 | 原版 13002 同一账号的用户列表读写通过。本地 13001 的公共 discovery 在独立实例启停前后逐字节一致，issuer 为 `https://auth.quukk.com/oidc`。 |
| 协议校验 | 错误 verifier 被拒绝。错误 discovery issuer、错误 ID Token 签名被浏览器 SDK 拒绝。原入口与新入口的刷新令牌和管理 API JWT 双向互拒。 |
| 网关隔离 | 匿名和普通成员的门禁、精确 UniHub 回调、Cookie 白名单、上游 401 和防嵌套响应头均通过。 |

浏览器验收脚本为 `browser-smoke.cjs`，网关测试可用 `NGINX_BIN=/path/to/nginx python3 test-nginx.py` 独立复跑。Core、Console、Experience、Account 四个包的 TypeScript 检查均通过。本地验收使用原生 Node 进程；独立 Linux 镜像与生产发布由父项目负责。
