# @unihub/sdk

UniHub JSON 协议的零依赖 TypeScript 客户端。它只负责协议：构造请求、校验响应形状和错误码。它不包含 UI、本地存储、令牌刷新、权限判断、重试或事务恢复；这些必须由接入产品按自己的业务规则实现。

当前状态：`private: true`，尚未发布到 npm。本包从 `web/src` 直接以源码形式引入，因此 `package.json` 中 `exports` 指向 `src/index.ts`。

## 使用方式

```ts
import { UniHubClient } from '@unihub/sdk';

const client = new UniHubClient({
  baseUrl: 'https://unihub.example.com',
  clientId: 'product-headless',
  redirectUri: 'https://product.example.com/unihub/callback',
  credentials: 'include', // 浏览器会话走 Cookie；服务端调用使用 Bearer
});
```

浏览器场景由 UniHub 控制台会话提供身份，服务端场景使用产品凭据换取访问令牌。两种方式不能混用：同一个请求不要同时携带 Cookie 与 Bearer。

## 导出面

类型与接口（节选，完整列表见 `src/index.ts`）：

- 账户与会员：`Account`、`Subscription`、`Offer`、`Batch`、`Order`、`Wallet`、`Operation`
- 身份：`Registration`、`RegistrationResult`、`TokenSet`、`MfaPending`、`IdentityProfile`、`ProductAccess`
- 计费：`BillingProfile`、`BillingPreset`、`BillingAsset`、`FundingMode`、`ModelCredential`
- 凭据：`APIKey`、`APIKeyGrant`、`APIKeyRequest`、`UsageBudget`
- 客户端：`UniHubClient`、`UniHubError`

主要方法：

| 方法 | 用途 |
| --- | --- |
| `passwordLogin` | 以用户名密码换取 PKCE 授权码；开启两步验证时抛出带 `mfa` 上下文的错误 |
| `mfaLogin` | 用 `mfa.token` 与一次码完成登录 |
| `productAccess` | 读取当前用户在某产品/实例内的角色与权限 |
| `applications` | 列出当前用户可进入的产品入口 |
| `accounts`、`subscriptions`、`credits`、`wallets`、`orders` | 读取账户、会员、额度、钱包与订单 |
| `createAPIKey`、`revokeAPIKey`、`usageBudget` | 管理产品范围内的长期凭据与预算 |

金额与额度统一使用整数小数字符串（现金 `CNY/6`、额度 `CREDIT/6`）。不要把它们转成浮点数再回写，也不要假设存在隐式兑换。

## 错误处理

所有失败都抛出 `UniHubError`，携带 HTTP 状态、机器可读 `code` 和可选的 `retryAfter`。调用方应当：

- 对 `409 conflict` 重新读取当前版本后再决定，不要盲目重试。
- 对 `operation_pending` / `UNKNOWN` 保持幂等键，先查询既有结果。
- 对 `429` 遵守 `Retry-After`，不要立即重试。

## 发布到 npm 的前置条件

1. 明确许可证类型（当前仓库为保留所有权利；发布公开包前必须取得书面授权）。
2. 确认包名 scope（`@unihub` 是否已注册或改用组织 scope）。
3. 补充浏览器与 Node 双环境打包（当前仅导出 TS 源码）、版本号策略和变更记录。
4. 增加 `files` 白名单，避免把测试或内部路径打进发布物。
5. 完成至少一个外部接入产品的端到端验收后，再提升版本号并发布。
