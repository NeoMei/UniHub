# 归一（UniHub）

<img src="branding/assets/unihub-logo-v1.png" alt="UniHub" width="300">

归一（UniHub）统一用户与计费平台，为多个产品提供共用的用户、企业团队、会员订阅、钱包和模型额度服务。

UniHub 提供独立后台 JSON API，以及可嵌入接入产品的用户基础模块。产品可复用登录、会话、自动建立业务用户和权限检查，再扩展自己的页面、资源与业务规则；也可通过独立客户端调用账户和计费服务。统一身份和产品角色由服务端校验，产品继续执行资源权限，不必嵌入 UniHub 用户中心。

当前版本：**v0.4.9（统一产品用户基础与复用源码预览版）**。充值与会员资金仍为 sandbox；本版身份与权限验收采用真实本地 Casdoor 和隔离产品环境，生产部署及既有模型/支付验收边界见[版本说明](docs/releases/v0.4.9.md)与[变更记录](CHANGELOG.md)。

## 命名约定

- 中文名称：**归一**
- 英文名称：**UniHub**
- 项目显示名：**归一（UniHub）**
- 工程标识：`unihub`
- 工程目录：`/Users/neomei/项目/codexprojects/unihub`
- 中文描述：统一用户与计费平台

后续包名、服务前缀和部署标识使用 `unihub`；中文产品界面使用“归一”，英文品牌保留 `UniHub`。

## 项目资料

- [产品用户模块、源码初始化与双模式接入](docs/integrations/product-user-foundation.md)
- [平台后台管理与权限矩阵](docs/platform-administration.md)
- [产品角色与用户管理 API](docs/product-user-api.md)
- [可独立引用的 Go 用户模块](packages/userkit-go/README.md)
- [领域术语](CONTEXT.md)
- [品牌与界面规范](branding/README.md)
- [品牌界面验收](docs/branding-validation-2026-10-24.md)
- [品牌任务三轮审查与补漏](docs/branding-audit-2026-10-24.md)
- [首版设计](docs/superpowers/specs/2026-10-24-unihub-design.md)
- [设计审查](docs/design-review-2026-10-24.md)
- [可组合收费配置](docs/composable-billing.md)
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

## 产品矩阵用户基础

本版提供统一产品目录、默认成员资格、按产品与实例分配的角色、全局用户停用、应用入口和用户管理界面。普通注册不会授予管理员权限；产品管理授权独立于付费账户的团队角色和会员权益。

新产品可以从源码生成一个独立可运行的 Go 项目：

```sh
python3 scripts/product_user_init.py \
  --output /tmp/notes-starter \
  --product-id notes \
  --module example.com/notes
```

生成项目包含浏览器 OIDC 登录、同一业务用户的幂等初始化、可扩展的资源权限策略、个人笔记本示例、测试、产品声明和已校验的共享模块源码。产品管理员先通过受保护的登记工具配置精确客户端与回调，接入产品再填写自己的私密配置；初始化工具不会自动获得平台管理权限或注册远端应用。详见[完整接入步骤](docs/integrations/product-user-foundation.md)。

共享模块支持 UniHub 身份提供方和产品自有的标准 OIDC 身份提供方，两种模式复用产品业务用户与资源策略。源码模板使用单进程持久化示例，部署到多实例前需要接入自己的数据库事务及共享会话设施。Go 模块尚未发布，当前采用带提交和摘要的源码快照。

LLMRouter 已在本地实现共享模块接入、浏览器单点登录、自动建立普通业务用户、服务端凭据保管和按当前产品角色管理渠道。产品用户不会获得原生钱包、原生令牌或不受限 root 权限。本地验收与发布资产不代表线上配置已切换。

## 可组合收费

各产品可选订阅赠送额度及加量包、充值余额按量使用、纯月/年订阅三个预设，也可组合会员与余额按量等规则。配置独立于身份客户端，产品通过同一API读取规则、权益、余额及用量，自行展示界面。现金模型消费统一从llmrouter产品钱包扣除，赠送额度单独记录，不会未经选择切换或混算。配置与接入方法见[可组合收费](docs/composable-billing.md)。

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
