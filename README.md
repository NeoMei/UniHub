# UniHub

面向终端用户和接入应用的统一账户和计费门户。当前版本提供 Logto 登录、应用登记及个人和企业账户管理。

## 本地运行

1. 使用 Node.js 24 或更新版本运行 `npm ci`。准备一个 PostgreSQL 数据库及有权建表的部署账户。
2. 在 Logto 创建传统 Web 应用，登记 `http://127.0.0.1:14000/auth/callback` 为回调地址，登记 `http://127.0.0.1:14000/` 为退出返回地址。
3. 将 `.env.example` 复制为 `.env`，填写 Logto 应用凭据和 `DATABASE_URL`。运行 `node -e "process.stdout.write(require('node:crypto').randomBytes(32).toString('base64url'))"` 生成 `UNIHUB_SESSION_KEY`。
4. 为数据库选择 `cn/CNY` 或 `us/USD`。首次启动写入账区绑定，后续启动校验配置。国内和美区分别使用独立数据库。
5. 保证 `LOGTO_ISSUER` 可达，运行 `npm start`，打开 `http://127.0.0.1:14000`。默认监听回环地址。部署时为 UniHub 和 Logto 配置 HTTPS。

平台管理员由 `UNIHUB_OPERATOR_SUBJECTS` 指定。这是包含当前 Logto 签发主体 `sub` 的 JSON 字符串数组，例如 `["实际的Logto用户ID"]`。登录后，`GET /api/session` 返回 UniHub 内部用户 ID、可选账户、当前付款账户和 CSRF 令牌。门户内可创建企业账户并切换付款账户。以下使用门户 Cookie 的写入 API 接受 `X-CSRF-Token` 请求头。门户原生表单使用 `csrf` 字段。

## 接入应用

- 平台管理员通过 `POST /api/admin/apps` 提交 `name`、`redirectUris` 和可选 `enabled`。响应返回应用 ID 与一次可见的 `clientSecret`，由应用后端保存。
- 平台管理员通过 `PUT /api/admin/apps/:appId` 提交完整的 `name`、`redirectUris` 和 `enabled`。使用 `PUT` 或 `DELETE /api/admin/apps/:appId/operators/:userId` 管理单个应用的运营权限。平台管理员及指定运营人员可读取 `GET /api/admin/apps/:appId`。应用登记和运营授权当前通过 API 管理。
- 应用生成随机 `state` 并与浏览器登录事务绑定，然后导航至 `GET /login`，传入 `app_id`、`redirect_uri` 和 `state`。回调必须逐字匹配登记值，可用 HTTPS 或回环 HTTP。回调地址禁止用户名、密码、片段和预设的 `code`、`state` 参数。
- 应用回调收到 `code` 和 `state` 后先核验自己的 `state`。应用后端用 HTTP Basic（应用 ID 和客户端密钥）向 `POST /api/apps/login/exchange` 提交 JSON `{ "code": "...", "redirectUri": "..." }`。两分钟内可兑换一次，返回 UniHub `userId`。接入应用用它建立自己的会话。

## 企业账户

使用 `POST /api/accounts/enterprises` 创建企业，`POST /api/session/account` 以 `accountId` 切换付款账户。企业管理员通过 `GET /api/accounts/:accountId/members` 查询成员，通过 `PUT` 或 `DELETE /api/accounts/:accountId/members/:userId` 授权或移除。授权提交 `role`、`canRecharge`、`canPurchase` 和 `hasSeat`。企业所有者可管理管理员及普通成员。管理员可以管理普通成员，并只能授予自身持有的充值与购买权限。

`GET /api/active-account/permissions/:action` 供当前付款账户做阶段一权限检查，支持 `recharge`、`purchase`、`issue-key`。充值和购买权限分别设置。普通成员购买还需有效席位，企业所有者与获准的管理员可在无席位时为企业采购。企业成员领用 Key 需要席位。成员被移除时，原付款账户失去授权，用户可以用原会话切回个人账户。

## 测试

使用可以创建 Schema 的专用 PostgreSQL 测试数据库运行 `UNIHUB_TEST_DATABASE_URL=postgresql://… npm test`。测试用例创建并清理各自的 Schema，使用本地签名 OIDC 服务覆盖登录、退出和应用交接。

[开源集成实施方案](docs/architecture.md) · [后台 API 评估](docs/integration-api-assessment.md) · [开发任务拆分](docs/superpowers/plans/2026-11-26-unihub-rebuild.md)
