# UniHub 管理后台入口

| UniHub 路径 | 浏览器打开的后台 |
| --- | --- |
| `/llmrouter` | 万流的原生控制台。页面使用独立来源 `https://llmrouter.quukk.com/dashboard`，在框架内登录及切换渠道。刷新外层地址会回到控制台首页。 |
| `/auth` | Logto 管理租户的控制台。UniHub 自身的 OIDC 客户端保留精确回调路径 `/auth/callback`。 |
| `/paylab` | Lago 中文账单后台，浏览器通过同域子路径访问账单 API。 |

UniHub 运营管理后台的「对账与运维配置」提供这三个入口。访问者先以列入 `UNIHUB_OPERATOR_SUBJECTS` 的身份登录 UniHub，然后使用对应后台的原生账号。未登录时，在原入口显示 UniHub 登录链接，点击后在新窗口完成身份认证。已登录的普通用户收到 403。

将 `common.conf` 与两个后台各自的 location 片段包含在 UniHub 的 **HTTPS server** 内。该 server 需继续拒绝来自公网的 `/internal/` 请求。Nginx 通过内部子请求核验 UniHub 会话。仅 `/auth/callback` 精确交给 UniHub。部署前运行 `nginx -t`。

验收时应分别检查匿名浏览器打开三个地址、未登录的 API 调用、普通账号与平台管理员。管理员登录后，在万流打开渠道列表，在 Logto 打开用户列表，在 Lago 打开发票列表。核对浏览器地址和请求来源，同时确认普通用户的 Logto issuer 仍返回原 discovery 文档。
