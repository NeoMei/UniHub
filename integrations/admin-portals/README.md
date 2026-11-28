# UniHub 管理后台入口

| UniHub 路径 | 浏览器打开的后台 |
| --- | --- |
| `/llmrouter` | 万流的原生控制台。页面使用独立来源 `https://llmrouter.quukk.com/dashboard`，在框架内登录及切换渠道。刷新外层地址会回到控制台首页。 |
| `/auth` | Logto 管理租户的控制台。UniHub 自身的 OIDC 客户端保留精确回调路径 `/auth/callback`。 |
| `/paylab` | Lago 中文账单后台，浏览器通过同域子路径访问账单 API。 |

UniHub 运营管理后台的「对账与运维配置」提供这三个入口。访问者先以列入 `UNIHUB_OPERATOR_SUBJECTS` 的身份登录 UniHub，然后使用对应后台的原生账号。未登录时，在原入口显示 UniHub 登录链接，点击后在新窗口完成身份认证。已登录的普通用户收到 403。

将 `common.conf` 与两个后台各自的 location 片段包含在 UniHub 的 **HTTPS server** 内。该 server 需继续拒绝来自公网的 `/internal/` 请求。Nginx 通过内部子请求核验 UniHub 会话。仅 `/auth/callback` 精确交给 UniHub。部署前运行 `nginx -t`。

验收时应分别检查匿名浏览器打开三个地址、未登录的 API 调用、普通账号与平台管理员。管理员登录后，在万流打开渠道列表，在 Logto 打开用户列表，在 Lago 打开发票列表。核对浏览器地址和请求来源，同时确认普通用户的 Logto issuer 仍返回原 discovery 文档。

## Logto 的独立管理实例

在运行 Logto 的主机上，`logto.Dockerfile` 将通过 SHA-256 校验的构建包安装到固定摘要的 1.46.0 镜像中。构建上下文只需包含 Dockerfile 和 overlay 压缩包。

```sh
sha256sum -c SHA256SUMS
export LOGTO_PORTAL_IMAGE="unihub-local/logto-console:1.46.0-$(cut -c1-12 SHA256SUMS)"
docker build --network none -f /opt/unihub/v2/integrations/admin-portals/logto.Dockerfile -t "$LOGTO_PORTAL_IMAGE" .
docker compose --env-file /opt/logto/.env -f /opt/unihub/v2/integrations/admin-portals/logto-sidecar.yml up -d
```

独立 Compose 项目从现有 Logto 配置继承数据库连接与加密设置，并接入现有的 Docker 网络。管理租户仅监听主机回环端口 13003，默认租户的管理 API 经 13004 提供。现有 Logto 容器继续负责公共身份认证。Nginx 的 http 层加载 `logto/cookies.http.conf`，UniHub 的 TLS server 加载 `logto/locations.conf`。后者通过相对 Nginx 配置目录的 `logto/` 引入两个请求头配置片段。
