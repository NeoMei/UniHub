# UniHub

<img src="branding/assets/unihub-logo-v1.png" alt="UniHub" width="300">

UniHub 统一用户与计费平台，为多个产品提供共用的用户、企业团队、会员订阅、钱包和模型额度服务。

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
- [账户与资金基础实施计划](docs/superpowers/plans/2026-10-24-account-wallet-foundation.md)
- [基础 API](docs/account-wallet-api.md)
- [本次验收与剩余范围](docs/account-wallet-foundation-validation.md)

当前优先完成自有产品的统一用户与充值计费闭环，首个接入产品为 SuperWagie。第三方账户连接、第三方 MCP 与 API 接入依次作为后续扩展。

## 当前进度

已落地第一阶段的账户与资金基础 API：Casdoor access token 验证、个人/团队账户、邀请与角色、充值订单、测试支付签名通知、统一钱包划拨、Formance 记账和后台恢复。业务数据保存在 PostgreSQL，现金余额以 Formance 为准。

现有登录与身份管理界面已使用 UniHub 专用 Logo、浏览器图标、页脚与主题；浅色/深色界面及真实 PKCE 登录已验证。

本地开发启动：

```sh
make dev-setup
make migrate
make api
# 在另一终端打开 UniHub 登录页，令牌仅保存在本地忽略目录
make dev-login
```

开发依赖只绑定本机端口。API 默认 `http://127.0.0.1:8088`，付费账户与钱包业务界面尚未实现。当前付款方式为明确标注的 sandbox，测试到账不代表真实收款。生产模式禁止 sandbox；真实支付通道尚未接入。

会员/续费、模型额度、自动充值策略、退款业务、运营界面，以及 SuperWagie 等产品接入仍属于后续第一阶段切片。当前基础服务通过测试，不等于整个平台已完成或可直接生产上线。
