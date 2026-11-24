# 应用接入与管理

UniHub 平台超管在「平台管理 → 应用接入与管理」登记应用、签发服务凭据、核对试点并发布。用户完成账号关联并获得该应用的访问权限后，可以在「我的应用」打开应用。

## 选择接入方式

| 应用现状 | 接入方式 |
| --- | --- |
| 已有独立账号、工作区和权限体系 | 使用应用接入向导登记原账号登录。应用验证原账号，UniHub 负责账号关联、应用入口及浏览器会话交接。 |
| 使用 UniHub 作为认证来源，或需要模型、计费、钱包、支付能力 | 按[第三方接入说明](unihub-third-party-integration.md)选择认证及服务能力。平台管理的「三方接入」提供部署配置准备。 |
| 已由部署配置接入 | 在管理列表查看实例、入口、登录客户端、回调与服务配置状态。现行部署文件负责维护这些记录。 |

## 登记已有账号的应用

1. 在管理页新建草稿，填写产品标识、实例、显示名称、站点 Origin、打开地址、回调地址和原账号登录地址。三个地址须与登记的 Origin 一致，生产使用 HTTPS。发布前通过应用自己的发布流程部署回调和原账号登录实现。
2. 签发服务凭据，通过部署的密钥注入机制传给应用后端。凭据只在签发时显示。应用可使用账号关联、会话交接和源账号状态同步接口。平台只保留凭据的校验摘要。
3. 在 UniHub 开启试点，在应用端配置试点用户。使用管理页的原账号登录地址开始测试。在没有 UniHub 登录态的浏览器窗口中，先从原账号登录进入 UniHub，保留 UniHub 登录态并清除源应用登录态，再从「我的应用」打开应用，核对回到同一个原账号。管理页的两个核验项分别由 UniHub 会话签发和回调会话交换产生。
4. 两项核验通过后，在 UniHub 发布。原账号登录入口随后出现在 UniHub 登录页。应用团队按验证结果扩大自身试点范围。应用级访问仍由当前账号关联和权限决定。
5. 暂停会从登录页和「我的应用」隐藏入口，并停止新的身份交接。源账号状态同步继续生效。恢复时重新开启试点并复测双向登录。已签发的会话依现有授权、注销和到期规则处理。可在草稿或暂停期间换发服务凭据，换发后立即部署新凭据。

## AgentWiki 案例

AgentWiki 在站点上验证原账号，用稳定的 AgentWiki 用户 ID 关联 UniHub 身份，并保留空间权限、Agent 凭据及审核。已经各自存在的账号通过双方认证并由本人确认绑定。同邮箱只是提示核验的线索。UniHub 关联后授予最小应用访问权限，原应用决定用户能打开哪些空间和页面。

| 字段 | AgentWiki 当前部署示例 |
| --- | --- |
| 产品 / 实例 | `agentwiki` / `default` |
| Origin | `https://agentwiki.quukk.com` |
| 打开地址 | `https://agentwiki.quukk.com/api/auth/unihub/start?returnTo=%2Fdashboard` |
| 回调地址 | `https://agentwiki.quukk.com/api/auth/unihub/callback` |
| 原账号登录地址 | `https://agentwiki.quukk.com/unihub?entry=hub` |

同类应用可实现以下行为，或参照 [UniHub 运行时适配说明](userkit/runtime-adapter.md)复用相应接口。

- 原账号验证成功后，应用使用已登记的客户端标识、原用户 ID、认证事件及当前认证版本完成关联与会话交接。应用自行处理首次绑定选择、账号冲突及登录前的试点限制。
- 从 UniHub 打开时，应用使用已登记的回调、浏览器状态与 PKCE 验证进行会话交换，并加载原用户在应用内的权限。
- 源账号锁定、删除、密码版本变化和本地会话退出，需要从应用同步到 UniHub。保留应用原有身份及资源授权，验收时用原凭据检查原有访问。


## 适配器验证顺序

| 验收点 | 产品端及 UniHub 的交互 |
| --- | --- |
| 识别原账号 | 产品后端调用 `POST /internal/v1/product-runtime/identity/enroll` 或 `link/start`、`link/preview`、`link/complete`，按验证结果创建或明确绑定身份。 |
| 原账号进入 UniHub | 浏览器进入 `/auth/product/start`，产品后端验证会话后调用 `identity/authenticate` 和 `identity/browser/approve`，浏览器在 UniHub 完成交接。 |
| UniHub 打开原应用 | 浏览器进入 `/auth/product/authorize`，应用按登记回调取得授权码，在后端调用 `identity/session/exchange`，确认原用户 ID 后建立应用会话。 |
| 生命周期与退出 | 产品通过 `identity/source-lifecycle` 和 `identity/source-session/revoke` 同步账号或会话变化，通过 `identity/session/verify` 检查仍生效的来源会话。 |

原账号接入只获得身份相关的服务权限。使用中心 OIDC、模型和计费的应用按前面的三方接入说明配置对应的客户端与权限。
