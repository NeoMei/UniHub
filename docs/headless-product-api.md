# 独立产品 Headless API 接入

UniHub 提供身份、账户/团队、产品钱包、商业订单、共享模型额度和调用查询的后台 JSON API。产品自己持有页面、交互和功能判断；不需要打开 UniHub 控制台。服务端始终复核身份、成员关系、scope、财务角色和资源归属，产品界面显示某个按钮不会授予权限。

机器契约见 [OpenAPI](../api/openapi.yaml)（[当前服务器](https://unihub.quukk.com/openapi.yaml)），本地 TypeScript SDK 在 [packages/sdk](../packages/sdk)。SDK 为 `@unihub/sdk` 0.2.0，private，尚未发布；零运行时依赖，使用 Node 24、Bun 或浏览器原生 fetch/WebCrypto。源码入口是 `packages/sdk/src/index.ts`，由产品构建工具直接编译，或复制到工作区依赖中。SDK不提供UI、不推断产品业务权限、不存管理密钥。

## 每个客户端独立登记

受控部署配置 `PRODUCT_CLIENTS_FILE` 指向包含严格 JSON Client 数组的受控文件，每个 client_id 是独立且精确的 OIDC audience，不可复用控制台、运营或其他产品 audience。一个产品可有多个 client。新产品 ID 为 `^[a-z][a-z0-9_-]{0,63}$`，不受最初种子产品列表限制。产品不能自行申请 scope；运营审批后发布配置。

```json
[{"product_id":"my-product","client_id":"my-product-web","application":"my-product-web","organization":"unihub","redirect_uris":["https://app.example.com/login/callback"],"origins":["https://app.example.com"],"scopes":["accounts:read","accounts:write","members:read","members:write","catalog:read","subscriptions:read","credits:read","wallets:read","recharges:read","recharges:write","transfers:write","operations:read","orders:read","orders:write","settings:read","settings:write","refunds:write","usage:read","model-credentials:write"],"billing_products":["my-product","llmrouter"],"password_login":false,"registration":false}]
```

上例能力清单只是审批示意。两个身份开关默认 false，开启还要求服务端私有 IdP 配置、已核验的审计脱敏、适配器支持的组织/应用策略和 IdP 能力；将 JSON 开关设 true 不表示部署验收已完成。配置不得含密码、client secret、支付密钥或模型服务密钥。未知字段和 scope、重复 audience、通配 URI 均拒绝。redirect 必须精确匹配，使用 HTTPS 或本机 loopback HTTP；Origin 使用精确 scheme/host/port，不带路径。

`billing_products` 缺省仅本产品。显式增加 `llmrouter` 可让产品界面销售获准的公共加量包；不能由请求参数扩大。旧 `OIDC_PRODUCT_CLIENTS` 保留原七条能力（包含模型凭证签发），不会自动获得团队、财务或报表能力。

## 注册、登录与令牌

产品使用自己的用户名/密码表单，调用以下三个 JSON POST，不能把 Casdoor 管理 API 或管理 secret 放入产品端：

| 路径 | 机器行为 |
| --- | --- |
| `/v1/identity/registrations` | 固定 client_id、username、password、可选 email/display_name，加调用者生成的 `Idempotency-Key`；201 或幂等读取 200 `status=CREATED` 后仍需登录；202 `status=UNKNOWN` 是结果待核实 |
| `/v1/identity/authorize/password` | 固定精确 client/redirect，scope 固定 `openid profile email`，随机 state/nonce，S256 code challenge；返回 authorization_id、code、state |
| `/v1/identity/token` | authorization_code，原 authorization_id/code、精确 client/redirect、code_verifier；单次消费，返回已由服务端验证的 access/id token |

注册密码须符合当前组织策略；适配器至少要求 12–128 字符，含 ASCII 大小写和数字，UTF-8 不超过 72 字节，不含用户名或可识别的邮箱本地部分。更严格的组织策略仍会生效。注册幂等重放不修改原密码；UNKNOWN 时只用原 key 和相同非敏感参数读取原结果，不换 key 再建用户。

登录的 MFA、改密、同意或其他策略要求返回 `409 interaction_required`，包含 interaction.kind；产品应显示待支持交互，不能绕过。token 交换返回 `authorization_result_unknown` 时不得自动重放授权码。当前没有 GET 公开 client configuration、refresh token 或服务端 logout 接口。SDK 的 clearAccessToken 只清当前对象内存，不撤销 IdP 会话。

```ts
import { UniHubClient, UniHubError } from './packages/sdk/src/index.ts';
const hub = new UniHubClient({
  baseUrl: 'https://hub.example.com',
  clientId: 'my-product-web',
  redirectUri: 'https://app.example.com/login/callback',
});
// 表单由产品持有。SDK在内存生成verifier/state/nonce并核验state。
await hub.passwordLogin({ username, password });
const me = await hub.me();
const accounts = await hub.accounts();
const resources = await hub.resources(accounts[0].id);
```

SDK不持久化 token、不写 Cookie/localStorage、不自动刷新。BFF 可在自己的安全会话中持有 token，通过 `setAccessToken` 注入；浏览器产品需要自行设计其内存会话生命周期。不要把密码或 token 放进日志、URL、追踪事件和持久化状态。

## Scope 与领域权限

| Scope | API |
| --- | --- |
| `accounts:read` / `accounts:write` | me/accounts/capabilities / 创建团队；仅 me 也可用 identity:read |
| `members:read` / `members:write` | 成员和待接受邀请读取 / 邀请、接受、角色变更和撤销 |
| `catalog:read` | 产品与已发布商品目录 |
| `subscriptions:read` / `credits:read` | 本产品订阅和 resources / 共享模型额度批次与汇总 |
| `wallets:read` / `recharges:read` / `recharges:write` | 余额 / 原充值单 / 建充值单与显式 sandbox settle |
| `transfers:write` / `operations:read` | 统一钱包向获准产品划拨 / 查原账务操作 |
| `orders:read` / `orders:write` | 订单列表 / 购买、升级报价和接受 |
| `settings:read` / `settings:write` / `refunds:write` | 统一钱包 funding/renewal 授权读取/变更 / 原订单退款 |
| `usage:read` | 来源产品调用列表、明细和汇总 |
| `model-credentials:write` | 本来源产品短期模型凭证签发/撤销 |

财务查询与命令仍须账户 owner 或 billing_admin。admin 管理普通成员不等于财务权限；新 owner 必须本人接受邀请；最后 owner 不可移除。邀请列表只含仍有效的待接受项，最多最近 100 条，没有通用分页。GET `/v1/invitations` 只看本人收件，GET `/v1/accounts/{account}/invitations` 仅团队 owner/admin 查看。

产品列表只返回获准资源，详情和命令先检查产品归属。请求体不能自称 actor、其他 account、费用或最终 usage。模型凭证 origin 缺省由服务端填写；显式其他 origin 被拒绝，撤销同样约束产品。可信预占/结算只对独立 llmrouter 服务身份开放，不属于产品 SDK。

## 资金、订单和共享额度

现金为 CNY/6，模型额度为 CREDIT/6。所有金额、token usage、count 都是规范十进制整数字符串；SDK 不调用 Number 转换数量。`"1000000"` 是 1 元现金或 1 个显示额度单位，两者不能互相兑换。外部充值还要求整分。

```ts
const order = await hub.createRecharge(accountId, 'my-product', '10000000', savedRechargeKey);
// 创建单不表示到账；真实渠道尚未配置时返回payment_not_configured。
const state = await hub.getRecharge(accountId, order.id);
const purchase = await hub.purchase(accountId, offerId, savedPurchaseKey);
// 独立呈现purchase.payment_status和purchase.fulfillment_status。
```

产品可向自己的或显式获准的产品钱包建充值单。直接用产品 token 为统一钱包（product_id 空）建单、读取、模拟结算暂时拒绝，因为旧充值记录缺少可信来源 client 归属；控制台统一充值保留。wallets:read 的财务用户可看到同账户统一钱包余额和获准产品余额。划拨仅从该统一钱包到获准产品。

调用者显式持有稳定幂等键。SDK没有财务重试逻辑；网络失败、409 operation_pending、UNKNOWN 都查原对象和原 key，不换 key 重付。返回 202 只表示异步命令/未知结果，不能显示为支付成功；账务操作仅 APPLIED 表示账本确认。退款返回 refund_requires_review 可能已冻结来源额度，需要读取原订单。错误为 `UniHubError{status,code,body,headers}`；SDK不会吞掉错误或将它变成成功。

`sandboxSettleRecharge` 仅供显式测试，不会在创建充值后自动调用；它运行真实账务命令恢复路径，但不是支付渠道到账证明。生产真实支付尚未集成。

共享模型额度继续是同账户 llmrouter 公共池，跨获准来源批次分配。产品 GET credits 返回每个真实批次的账本数量、有效期和冻结状态，省略其他产品、订单、订阅和来源配置；批次 ID 为实际 opaque ID。不能将本产品订阅额度误称为账户全部余额。resources 的 spendable 仅累计有效且未冻结 available，reserved/frozen 分开；财务现金字段对普通成员省略。as_of 是读取完成时刻，不承诺 PostgreSQL 和账本的原子快照。

## 调用分页与准确性

`hub.calls(account,{from,to,model,limit,cursor})` 与 `hub.usage(account,{from,to,model})`。from 含、to 不含，RFC3339；默认最近 30 天，最长 90 天。limit 默认50、范围1–100。产品 origin 由已验证客户端强制绑定，不能查询另一个产品。

响应明确 scope.visibility：member/admin 是本人；owner/billing_admin 是同来源产品的账户范围。财务角色才有 held/charged，其他用户不会拿假零。provider_cost、内部差额、credential/digest/allocations、prompt/response 均不返回。

调用列表的 total 和汇总来自单次 PostgreSQL 查询快照中的完整授权筛选集，不是当前页长度。unknown_time 单列无法证明时间的历史记录。只有已结算调用的真实 charged 计入消费；UNKNOWN/MISSING_USAGE 单列，usage_complete 说明缺口。

后续页保持相同 model/origin，用 next_cursor；未显式重复的时间由签名游标恢复。每次重查成员关系和当前 role；游标绑定 account/actor/filter，不能跨账户。进程重启使旧游标失效，需要从首屏重新开始。`consistency=live` 意味着每次请求精确，后续页或独立 summary 请求可能反映新提交/结算，不是冻结报表。

## CORS 与验收

浏览器直连使用精确登记 Origin。OPTIONS 只允许 Authorization、Content-Type、Idempotency-Key；没有 Allow-Credentials。实际产品 Bearer 请求再次按该 client 的 Origin 复核。身份三个 JSON POST 可在无 Bearer 时读响应，但仍复核 body.client_id 对应的 exact Origin。BFF/原生无 Origin 可调用。控制台 Cookie/CSRF 规则不会因为产品 CORS 放宽。

协议和安全回归已在隔离 PostgreSQL + 真实 Formance 验证；SDK 测试启动真实本地 HTTP server，核对 state、PKCE、固定client/redirect、幂等键、整数串、结构化错误和无自动重试。测试命令：

```sh
cd packages/sdk
npm test
npm run typecheck
```

完整本地 JSON 用户流程由 `scripts/headless_e2e.py` 提供（由部署验收环节维护）；运行前按本地部署说明启动测试 Casdoor/PostgreSQL/Formance，并使用批准的隔离测试客户端与沙盒商品。不要将协议测试描述为真实生产 Casdoor、双站点浏览器或真实支付渠道已经验收；以该脚本当前实际执行日志和部署验收报告为准。

## 通用产品登记脚本

使用 `scripts/product_client.py` 为新产品生成公共 registry 与 Casdoor 应用计划，无须修改 Go 源码或种子枚举。Python 3 标准库即可运行。它只管理 `unihub` 私有组织中明确命名的独立应用；拒绝 built-in、共享应用、控制台 `app-unihub`、客户端归属冲突或不带本工具产品归属标记的既有应用。不会编辑组织、用户、证书或其他应用。

先离线准备并审查计划，不读取管理秘密，也不修改实际 registry：

```sh
python3 scripts/product_client.py prepare \
  --registry .local/product-clients.json \
  --output .local/my-product-plan.json \
  --product-id my-product \
  --client-id my-product-web \
  --application app-my-product \
  --redirect-uri https://app.example.com/login/callback \
  --origin https://app.example.com
```

默认仅 accounts:read/catalog:read，账务产品仅自身，注册与密码登录都关闭。`--scope`、`--billing-product` 可重复并形成显式审批列表；`--password-login` 和 `--registration` 分别显式开启适配器能力。`--certificate` 指定运营已建立的 RS256 证书，默认现有 `cert-built-in`；脚本不创建或输出证书私钥。若应用存在额外 provider/group/tag 安全策略则拒绝改写，需人工审查并使用新独立应用。

管理客户端须是独立、非共享的 `unihub` 私有组织应用，具备管理目标产品应用的授权；脚本通过只读应用与组织策略先验证这个边界。将 `HEADLESS_MANAGEMENT_CLIENT_ID` 和 `HEADLESS_MANAGEMENT_CLIENT_SECRET` 放在私有进程环境，不作为命令行参数或写入计划/前端 registry。随后显式应用并回读：

```sh
python3 scripts/product_client.py apply \
  --plan .local/my-product-plan.json --issuer https://identity.example.com
python3 scripts/product_client.py readback \
  --plan .local/my-product-plan.json --issuer https://identity.example.com
```

apply 只调用精确目标应用的 get/add/update API，保留既有 client secret，不输出 provider 响应或秘密。应用配置回读与计划一致后，才以原子替换方式发布公共 registry；prepare 之后 registry 发生变化会拒绝覆盖。已创建且一致的目标再次 apply 不重复创建。上游结果不明时脚本停止，不自动重发 mutation；先 readback，再根据原计划和实际目标重新 prepare/apply。

该脚本不重启或部署服务。将 `PRODUCT_CLIENTS_FILE` 指向发布后的文件，按正常流程重载 UniHub；启动会为受控 registry 的新 product slug 建立目录行，保留已停用产品和商业条款。可用 `python3 scripts/product_client.py --help`、各子命令 `--help` 查看参数，离线回归运行 `python3 scripts/test-product-client.py`。离线协议测试不能替代所用 Casdoor 版本、证书、管理授权和最终 JSON 登录流程验收。

已完成本地真实 Casdoor 补充验收（2026-10-25）：SDK 使用独立 headless 测试客户端完成密码 JSON 授权、64 位 code 的 S256 交换、me/accounts/resources 读回及内存 token 清除，无资金写入；通用接入脚本为独立 headlessonboard 产品创建指定私有应用、发布公共 registry，并完成再次 prepare/apply（零 POST mutation）和 readback。公开结果分别保存于 `.local/headless-onboarding/sdk-acceptance.json` 与 `onboarding-acceptance.json`，不含凭据。这些是本地受控环境结果，不代表生产密码流程、双站点浏览器或真实支付验收。

当前部署的公网 JSON 全流程、SDK、权限边界及重启读回结果见[2026-10-25验收记录](headless-product-api-validation-2026-10-25.md)。真实支付仍未配置；现有产品何时改用这些 API，由各产品分别实施。
