# Changelog

## 0.1.0 — 2026-10-25

UniHub 第一阶段预览版：为自有产品提供独立后台用户与计费服务。产品拥有自己的界面和功能判断，不依赖 UniHub 示例用户中心。

### 主要能力

- 纯 JSON 注册、密码认证和 S256 PKCE 令牌交换；独立产品客户端、精确 Origin、scope 与账户角色联合校验。
- 个人及企业团队账户、成员角色、邀请查询和接受。
- 产品钱包、统一钱包、显式划拨、独立自动补款/续费授权与可恢复账务操作。
- 月付/年付会员、升级和加量包；年付逐月发放，月度模型额度按月重置。
- llmrouter 共享模型额度池、短期模型凭证、可信服务预占/结算、调用分页和用量汇总。
- OpenAPI 3.1 契约、零依赖 TypeScript 源码 SDK、新产品登记和纯 API 验收工具。
- 可选的 UniHub 品牌用户中心；HTTPS 部署、审计脱敏、每日备份及隔离恢复检查。

### 验证

- 真实 PostgreSQL/Formance 全仓库 race 回归通过；SDK 协议与类型检查通过。
- 真实 Casdoor、公网 JSON/SDK 业务流程、账户权限隔离和重启持久化通过。
- 真实身份审计/日志敏感标记检查及升级前后物理备份恢复检查通过。

### 当前边界

- 当前服务器为明确标记的 sandbox 测试环境；尚未配置真实支付渠道、正式价格或真实模型供应商。
- 产品令牌暂不支持直接向统一钱包充值；获准产品钱包充值和统一钱包显式划拨可用。
- MFA、CAPTCHA、强制改密等交互尚无 API 续接，返回 interaction_required；没有公开 refresh 或服务端 logout API。
- TypeScript SDK 是私有工作区源码包，未发布至 npm；现有产品切换到这些 API 须分别实施。
- 细节和验收范围见 docs/headless-product-api.md 与 docs/headless-product-api-validation-2026-10-25.md。
