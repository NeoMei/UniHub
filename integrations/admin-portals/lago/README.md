# UniHub 的 Lago 管理入口

原生 Lago v1.57.0 中文管理界面挂在 `https://unihub.quukk.com/paylab/`。发布物是 `stage/` 的内容，含 HTML、图标、字体、WASM 和按需加载的页面。此目录仅本地暂存，由 Git 忽略。

## 主机接入

1. 将 `stage/` 的内容部署为主机 Nginx 可读的 `/srv/unihub-admin-portals/paylab/`，使 `/srv/unihub-admin-portals/paylab/index.html` 直接存在。主机使用标准 MIME 配置。
2. 在 `unihub.quukk.com` 的 TLS server 内与父项目的 `../common.conf` 一同 include 本目录的 `locations.conf`。所有外部私有入口先走运营员鉴权。SPA 和静态资源均由主机读取。原前端容器仅提供 `/paylab/env-config.js` 的公开运行时设置。
3. 按父项目流程执行主机配置检查和切换。此交付只做了本地切换模拟。

| 公开路径 | 处理方式 |
| --- | --- |
| `/paylab` | 鉴权后 308 到带斜线的路径，保留查询字符串 |
| `/paylab/...` | 由 React Router 处理组织、客户、账单和登录等路由 |
| `/paylab/assets/...`、生产 favicon | 精确读取文件，缺失返回 404 |
| `/paylab/env-config.js` | 读取 127.0.0.1:18780 的原生配置 |
| `/paylab/api/graphql` | 同源 POST，转给 127.0.0.1:18781/graphql |
| `/paylab/api/cable` | 同源 WebSocket，转给 127.0.0.1:18781/cable |
| 其他 `/paylab/api/...` | 鉴权后返回 404 |

Lago 使用独立的管理员登录令牌和组织 ID。上游请求头采用白名单，Cookie 被清空，后台 401 原样返回。ActionCable 使用 Lago 自己的协商子协议。静态响应禁止嵌入和共享缓存。源站的原生根站、对外 REST API 及 Webhook 配置保持独立。

## 重建

在本目录执行。需要 Git、Node 24、Corepack 和 ripgrep。脚本在独立临时 clone 中使用锁定的 pnpm，并要求输出目录尚不存在。

```sh
./build.sh "$LAGO_CHINESE_CHECKOUT"
```

`LAGO_CHINESE_CHECKOUT` 指向含下述两个提交的本地 Git checkout。默认输出到本目录的 `stage/`。可通过第二个参数更换目标目录，通过 `LAGO_RELEASE_DEPS` 复用锁文件一致的已安装依赖。

构建起点为前端 v1.57.0 的 `1929e99148849758d9f8bfd60fc2cc94c1ab95cc`。两个 `v1.57-chinese-*.patch` 从该版本与中文提交 `be954532ff3cbbd37ed68578ea4a8fc49587b757` 重建实际发布的移植版本。源站原生首页和中文资源与留存的发布物分别逐字节匹配。仅 `paylab.patch` 实现本次子路径适配。设置 `LAGO_FRONT_BASE_PATH=/paylab/` 启用它，默认构建继续使用根路径。

## 验收

`smoke.cjs` 加载本目录 Nginx 片段及父项目的 common.conf，再用回环端口替换外部依赖。Chromium 将正式主机名仅解析到本地 TLS 监听端口。运行需支持 SSL 和 auth_request 的 Nginx、OpenSSL、Playwright 和 graphql。使用独立临时目录安装测试依赖。

```sh
deps=$(mktemp -d)
npm install --prefix "$deps" playwright graphql@16
"$deps/node_modules/.bin/playwright" install chromium
NODE_PATH="$deps/node_modules" node smoke.cjs
```

可通过 NGINX_BIN 和 CHROME 指定可执行文件，通过 LAGO_ROOT_DIST 增测原有根路径产物。模拟服务只接受登录以及读取测试数据的操作。已验证中文密码登录、客户和账单数据显示、登录后恢复带查询参数和哈希的深链接、刷新、忘记密码页、404 页硬跳转、WebSocket 握手、缺失资源、越权拒绝、后台 401 和上游 Cookie 隔离。默认根构建也通过登录页及忘记密码页刷新检查。桌面 1440×900，另检查了 390×844 登录页。浏览器保留该原生版本的 Apollo 弃用提示和重复 ActivityLogDetails fragment 提示。

暂存包有 790 个文件，共 23,953,462 字节。两次独立构建的 790 个交付文件完全相同。首页 SHA-256 为 `fb88179be2d5d2e8e186eb5ba497484676a6b6bffec582e5209303de213984a2`。

源站只读探测验证了预期 Origin 下 GraphQL 的 Query 响应和 ActionCable 的 HTTP 101。公网新路径、真实用户登录、第三方 SSO、邮件送达、文件导出下载与财务写操作未做端到端验收。当前 API 的 LAGO_FRONT_URL 为 `http://127.0.0.1:18780`，密码重置邮件使用此值拼接链接；生产邮件链接和第三方回跳地址需单独对接。
