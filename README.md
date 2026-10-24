# UniHub

<img src="branding/assets/unihub-logo-v1.png" alt="UniHub" width="300">

UniHub 统一用户与计费平台，为多个产品提供共用的用户、企业团队、会员订阅、钱包和模型额度服务。

UniHub 的核心是独立后台 JSON API。接入产品自己实现所有页面、会话体验与功能判断，通过独立客户端调用注册、认证、充值、订单、权益、余额与用量服务；不依赖 UniHub 用户中心。现有用户中心是可选的示例客户端。服务端仍校验身份、产品授权范围和账户角色，保护资金与资源归属。

发布版本：**v0.1.0（首阶段预览版）**。当前在线服务为 sandbox；[版本说明](docs/releases/v0.1.0.md)与[变更记录](CHANGELOG.md)。

## 命名约定

- 项目显示名：**UniHub**
- 工程标识：`unihub`
- 工程目录：`/Users/neomei/项目/codexprojects/unihub`
- 中文描述：统一用户与计费平台

后续包名、服务前缀和部署标识使用 `unihub`；产品界面使用 `UniHub`。

## 项目资料

- [领域术语](CONTEXT.md)
- [品牌与界面规范](branding/README.md)
- [品牌界面验收](docs/branding-validation-2026-10-24.md)
- [品牌任务三轮审查与补漏](docs/branding-audit-2026-10-24.md)
- [首版设计](docs/superpowers/specs/2026-10-24-unihub-design.md)
- [设计审查](docs/design-review-2026-10-24.md)
- [产品接入核查](docs/integration-findings-2026-10-24.md)
- [后续扩展方向与第三方项目调研](docs/extension-roadmap-2026-10-24.md)
- [本地开发与 API 使用](docs/local-development.md)
- [服务器部署验收](docs/server-deployment-2026-10-24.md)
- [服务器运维与恢复](deploy/server/README.md)
- [账户与资金基础实施计划](docs/superpowers/plans/2026-10-24-account-wallet-foundation.md)
- [基础 API](docs/account-wallet-api.md)
- [独立产品接入与 TypeScript SDK](docs/headless-product-api.md)
- [OpenAPI 契约](api/openapi.yaml)（运行中的服务提供 `/openapi.yaml`）
- [独立产品 API 验收](docs/headless-product-api-validation-2026-10-25.md)
- [账户基础验收](docs/account-wallet-foundation-validation.md)
- [订阅、额度与控制台 API](docs/subscriptions-credits-api.md)
- [本阶段验收与剩余范围](docs/subscriptions-credits-validation.md)
- [多轮审查、修复证据与实施裁定](docs/subscriptions-credits-review.md)

当前优先完成自有产品的统一用户与充值计费闭环，首个接入产品为 SuperWagie。第三方账户连接、第三方 MCP 与 API 接入依次作为后续扩展。

## 当前进度

已实现账户、资金与订阅额度的本地闭环：Casdoor 登录、个人/团队账户与角色、测试充值、产品钱包和统一钱包、订阅购买、升级、加量包、按月发放模型额度、独立的续费/补款授权、退款核实与故障恢复。PostgreSQL 保存业务状态；现金与模型额度数量均以 Formance 为准。

后台已补齐纯 JSON 注册与密码认证、PKCE 换取令牌、独立产品 scope/Origin、团队邀请查询、资源快照和调用/用量报表，并提供零依赖 TypeScript SDK 与新产品登记工具。身份开关需要受控配置及真实审计脱敏验收；MFA 等尚未支持的交互返回明确错误。

可选用户中心提供概览、会员、模型额度、加量包、钱包、订单、团队和设置八个页面。界面使用已确认的 UniHub Logo 与主题，浏览器通过服务端 OIDC 会话登录。商品价格可配置；当前发布的示例商品明确标记为测试套餐。

```sh
make dev-setup
make migrate
make web-build
make api
# 打开 http://localhost:8088
```

`make test-integration` 启用真实 PostgreSQL/Formance 与 race 检查；`make web-test` 验证前端。CLI 调试仍可用 `make dev-login`。

SuperWagie 独立平台登录、个人/团队选择、会员与额度读取、控制台跳转以及 llmrouter 模型计费已通过完整 Web 界面本地验收。模型上游使用受控模拟，身份、业务服务与账本均实际执行。浏览器与财务证据、部署边界见[阶段验收](docs/subscriptions-credits-validation.md)。当前付款方式是 sandbox，测试到账不代表真实收款。生产模式禁止 sandbox；真实支付通道、正式价格、独立运营可视化后台、AgentWiki 等后续产品及商业部署仍未完成。
