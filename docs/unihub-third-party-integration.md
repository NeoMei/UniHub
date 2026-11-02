# UniHub 三方系统接入与充值计费指南

本文面向需要接入 UniHub 的第三方产品、SaaS、管理后台和模型应用。目标是让接入方能够：

- 使用 UniHub 作为统一身份、账户和产品授权中心；
- 在自己的页面中读取用户、团队、订阅、额度和钱包状态；
- 创建充值单、购买套餐、查询账务操作和处理退款状态；
- 对模型或其他可计量服务使用统一的额度/现金计费协议；
- 在保留原系统用户、密钥、余额和业务数据的前提下完成接入。

机器契约以仓库中的 [api/openapi.yaml](../api/openapi.yaml) 和部署环境公开的 /openapi.yaml 为准。TypeScript 客户端源码位于 [packages/sdk](../packages/sdk)，Go 产品用户组件位于 [packages/userkit-go](../packages/userkit-go)。两者目前都是仓库源码，尚未发布为公共 npm 或 Go 包。

## 1. 先确定接入模式

| 场景 | 推荐模式 | UniHub 负责 | 三方系统继续负责 |
| --- | --- | --- | --- |
| 已有成熟用户系统、密码、MFA、API Key 和余额 | 既有系统适配 | 中央身份映射、产品授权、账户/账务查询和统一结算 | 原用户 ID、原认证器、原本地资源、原 Key、原业务数据 |
| 新建系统，没有完整用户和计费模块 | userkit-go + UniHub API | 中央身份、产品授权、账户、钱包、订单和计费 | 产品用户表、业务资源、产品页面和产品权限细化 |
| 模型网关或 LLM 应用 | 产品 API + LLMRouter 计费协议 | 账户、模型凭证、额度/现金资产、预占和结算 | 上游供应商调用、模型请求、供应商响应和重试策略 |

身份映射和资金迁移是两个独立工作。邮箱或用户名相同不能证明两个账号属于同一人；旧余额也不会因为首次登录自动转移到 UniHub。

## 2. 总体架构

建议采用产品后端作为 BFF，浏览器只访问三方系统自己的会话接口：

~~~mermaid
sequenceDiagram
    participant U as 用户浏览器
    participant P as 三方产品后端
    participant H as UniHub
    participant I as 身份提供方
    participant L as 账本/计费服务

    U->>P: 产品登录或打开产品页面
    P->>I: Authorization Code + PKCE / 产品登录
    I-->>P: 已验证身份结果
    P->>H: Bearer API 请求
    H-->>P: 中央用户、产品授权、账户和计费状态
    P-->>U: 产品自己的 HttpOnly 会话
    U->>P: 购买/充值/发起业务请求
    P->>H: 使用原始 Idempotency-Key 调用
    H->>L: 创建订单、预占、结算或释放
    L-->>H: 账务状态
    H-->>P: 只返回已授权的业务结果
    P-->>U: 展示待处理、成功或需要核对的状态
~~~

三方后端必须保存自己的受保护会话和状态。UniHub 的 access token、refresh token、模型凭证和服务凭据不能进入浏览器 JSON、URL、普通日志或前端 localStorage。

## 3. 注册一个独立产品客户端

每个产品都要有独立的 product_id、client_id 和应用登记记录。不能复用 UniHub 控制台、运营客户端或其他产品的 audience。

### 3.1 产品注册信息

接入方先提交：

- 产品 ID，例如 notes、llmrouter；
- 独立 client_id，例如 notes-web；
- Casdoor/身份应用名，例如 app-notes-web；
- 精确回调地址，例如 https://notes.example.com/auth/callback；
- 精确 Origin，例如 https://notes.example.com；
- 运行实例，例如 default；
- 需要的 scopes 和可访问的 billing_products；
- 是否需要浏览器 SSO、产品密码登录或产品注册。

回调地址和 Origin 必须精确匹配，不允许通配符、路径模糊匹配或把非回环 HTTP 用于生产。注册文件不能包含密码、client secret、支付密钥或模型服务密钥。

### 3.2 离线生成登记计划

使用仓库脚本生成计划，先审查计划，再由有权限的运维环境执行 apply：

~~~sh
python3 scripts/product_client.py prepare \
  --registry .local/product-clients.json \
  --output .local/notes-client-plan.json \
  --product-id notes \
  --instance-id default \
  --client-id notes-web \
  --application app-notes-web \
  --redirect-uri https://notes.example.com/auth/callback \
  --origin https://notes.example.com \
  --browser-sso \
  --scope access:read \
  --scope identity:read \
  --scope accounts:read \
  --scope accounts:write \
  --scope members:read \
  --scope members:write \
  --scope catalog:read \
  --scope subscriptions:read \
  --scope credits:read \
  --scope wallets:read \
  --scope recharges:read \
  --scope recharges:write \
  --scope orders:read \
  --scope orders:write \
  --scope operations:read \
  --scope model-credentials:write
~~~

scope 应按最小权限原则填写。只有需要产品钱包充值时才申请 recharges:write；只有模型网关需要短期模型凭证时才申请 model-credentials:write。billing_products 默认是产品自身；访问其他产品的商品或钱包必须经过显式审批。

运维环境执行：

~~~sh
export HEADLESS_MANAGEMENT_CLIENT_ID='由运维环境注入'
export HEADLESS_MANAGEMENT_CLIENT_SECRET='由秘密存储注入'

python3 scripts/product_client.py apply \
  --plan .local/notes-client-plan.json \
  --issuer https://unihub.quukk.com

python3 scripts/product_client.py readback \
  --plan .local/notes-client-plan.json \
  --issuer https://unihub.quukk.com
~~~

apply 完成后还要把审查过的客户端计划发布到 UniHub 的受控 PRODUCT_CLIENTS_FILE，并按部署流程重载服务。readback 是确认身份应用和 registry 同时符合计划，不是测试页面能打开的替代品。

### 3.3 常用 scope

| Scope | 用途 |
| --- | --- |
| access:read | 读取产品能力和产品授权 |
| identity:read | 读取当前中央用户 /v1/me |
| profile:write | 修改显示名称、密码或头像（能力开启时） |
| accounts:read / accounts:write | 查看账户、创建团队 |
| members:read / members:write | 查看成员、邀请、接受邀请、修改和撤销成员 |
| catalog:read | 读取产品目录、计费配置和已发布商品 |
| subscriptions:read | 读取订阅和订阅权益 |
| credits:read | 读取模型额度批次和可消费汇总 |
| wallets:read | 读取账户钱包余额，仅财务角色可见金额 |
| recharges:read / recharges:write | 读取充值单、创建产品钱包充值单 |
| transfers:write | 统一钱包向获准产品钱包划拨 |
| orders:read / orders:write | 查询、购买、升级和接受报价 |
| operations:read | 查询异步账务操作 |
| settings:read / settings:write | 读取和修改 funding/renewal 授权 |
| refunds:write | 发起原订单退款请求 |
| usage:read | 查询本产品调用、汇总和预算 |
| model-credentials:write | 签发或撤销短期模型调用凭证 |

产品 token 不能访问平台后台、运营价格写接口或 UniHub 内部结算接口。产品拥有账户 owner 角色也不会自动获得平台管理员或运营权限。

## 4. 用户登录和身份映射

### 4.1 浏览器登录

推荐让产品后端完成 Authorization Code + PKCE，浏览器只持有产品自己的 HttpOnly、Secure、SameSite 会话 Cookie。产品后端再使用已验证的 UniHub Bearer 访问 /v1/*。

如果客户端显式开启 browser_sso，产品可使用注册的精确回调地址完成交互式 SSO。如果客户端显式开启 password_login，也可以通过产品自有登录表单调用 UniHub 的 JSON 密码授权接口；该能力不是默认开启项。

使用 TypeScript SDK 的最小示例：

~~~ts
import { UniHubClient, UniHubError } from '@unihub/sdk';

const hub = new UniHubClient({
  baseUrl: 'https://unihub.quukk.com',
  clientId: 'notes-web',
  redirectUri: 'https://notes.example.com/auth/callback',
});

try {
  // 仅在该 client 已批准 password_login 时使用。
  const tokenSet = await hub.passwordLogin({ username, password });
  // tokenSet 只交给产品后端的受保护会话层，不写入浏览器存储。
  const me = await hub.me();
  const accounts = await hub.accounts();
} catch (error) {
  if (error instanceof UniHubError && error.code === 'interaction_required') {
    // 展示 MFA、同意或其他身份交互，不要绕过交互重试。
  }
  throw error;
}
~~~

MFA、同意、密码策略或其他交互需要返回 409 interaction_required 时，产品必须让用户完成交互。授权码交换出现 authorization_result_unknown 时，不能换一个 code 或 key 自动重放。

### 4.2 中央用户和产品用户

首次用有效身份调用 GET /v1/me 时，UniHub 会按已验证身份建立或读取中央用户：

~~~sh
curl -fsS https://unihub.quukk.com/v1/me \
  -H 'Authorization: Bearer <access-token>'
~~~

产品应保存以下映射：

~~~text
central_user_id  <- UniHub /v1/me.id
issuer           <- UniHub issuer
subject          <- 已验证身份 subject
local_user_id    <- 产品自己的用户 ID
~~~

成熟产品可以继续保留原 local_user_id、密码、MFA、Passkey、API Key、模型限制和资源表；新产品使用 packages/userkit-go 的 Provider、Provisioner 和 ResourcePolicy 完成映射。

产品用户表必须对 (issuer, subject) 建唯一约束，并在一个数据库事务中完成“查找/创建产品用户 + 初始化产品资源”。并发首次登录必须返回同一个产品用户。邮箱、用户名、显示名称和前端传入的角色都不能作为唯一身份证明。

产品侧资源权限只能收窄 UniHub 的权限，不能把中央拒绝重新授予产品用户。中央 super_admin、finance_admin 或产品管理员不会自动变成三方系统的本地管理员。

### 4.3 产品授权

~~~http
GET /v1/me/product-access?product_id=notes&instance=default
Authorization: Bearer <access-token>
~~~

产品 token 的 product/instance 从 registry 推导，不能通过请求参数切换到其他产品。返回结果包含：

~~~json
{
  "issuer": "https://unihub.quukk.com",
  "subject": "central-user-uuid",
  "product_id": "notes",
  "instance_id": "default",
  "user": {"id": "central-user-uuid", "name": "Alice", "email": "alice@example.com"},
  "roles": ["member"],
  "permissions": ["product:use"],
  "version": 1,
  "active": true,
  "expires_at": 1793100000
}
~~~

每次受保护请求都应在服务端重新核对当前中央状态和产品权限，不能把 roles 或 permissions 作为浏览器提交的授权依据，也不应建立永久 allow cache。

## 5. 账户、团队和财务角色

~~~sh
# 当前用户能访问的个人和团队账户
curl -fsS https://unihub.quukk.com/v1/accounts \
  -H 'Authorization: Bearer <access-token>'

# 创建团队账户；需要 accounts:write
curl -fsS -X POST https://unihub.quukk.com/v1/accounts \
  -H 'Authorization: Bearer <access-token>' \
  -H 'Content-Type: application/json' \
  -d '{"name":"示例团队"}'
~~~

账户成员角色至少包括 owner、billing_admin 和普通 member。只有账户 owner 或 billing_admin 能执行资金、订单和退款操作；普通成员可按 scopes 读取自己的订阅权益和额度。最后一个 owner 不能被移除。

邀请按不可变中央用户 ID 定位：

~~~json
{"user_id":"central-user-uuid","role":"member"}
~~~

UniHub 不会因为邮箱相同自动合并用户，也不会把产品管理员、Casdoor 管理员或财务 operator 自动加入团队。

## 6. 充值模型

### 6.1 两类资产

UniHub 目前区分两类资产：

| 资产 | 含义 | 用途 |
| --- | --- | --- |
| CNY/6 | 现金微单位 | 产品钱包预付费、充值余额和现金按量调用 |
| CREDIT/6 | 模型/服务额度微单位 | 订阅赠送额度、加量包和额度按量调用 |

数量必须是规范的非负十进制整数字符串，不能用 JSON number、浮点数或科学计数法。1000000 表示 1 元现金，或 1 个显示额度单位；两类资产不能相加、兑换或隐式互换。外部充值还要求整分，1 分为 10000。

### 6.2 查询钱包和资源

~~~ts
const wallets = await hub.wallets(accountId);
const resources = await hub.resources(accountId);
~~~

resources 返回本产品订阅、额度、钱包和支付能力：

~~~json
{
  "account_id": "account-uuid",
  "product_id": "notes",
  "subscriptions": [],
  "credits": {
    "asset": "CREDIT/6",
    "pool": "llmrouter",
    "spendable": "480000000",
    "reserved": "20000000",
    "frozen": "0"
  },
  "wallets": [{"product_id":"notes","asset":"CNY/6","amount":"10000000"}],
  "payments": {"mode":"sandbox","real_payments_configured":false},
  "as_of": "2026-11-03T00:00:00Z"
}
~~~

spendable 只统计当前有效且未冻结的额度。available、reserved、frozen 必须分别展示，不能把钱包余额或已冻结批次当成可消费额度。

### 6.3 创建充值单

产品 token 只能为自己或 registry 明确批准的产品钱包创建充值单；产品 token 不能直接为统一钱包创建充值单。统一钱包充值和资金划拨属于平台/财务边界。

~~~http
POST /v1/accounts/{account}/recharges
Authorization: Bearer <access-token>
Idempotency-Key: recharge-account-20261103-0001
Content-Type: application/json

{"product_id":"notes","amount":"10000000"}
~~~

10000000 是 10 元现金微单位。TypeScript SDK：

~~~ts
const recharge = await hub.createRecharge(
  accountId,
  'notes',
  '10000000',
  'recharge-account-20261103-0001',
);
const readback = await hub.getRecharge(accountId, recharge.id);
~~~

创建充值单不等于到账。真实支付通知确认后，UniHub 才创建账务操作。只有账务操作状态 APPLIED 才表示账本已经确认：

~~~ts
let operation = await hub.getOperation(accountId, recharge.operation_id!);
// APPLIED: 已记账；PENDING/SUBMITTING: 继续查询；
// REJECTED/CANCELED: 终态失败；UNKNOWN: 先核对原操作，不能换 key 重试。
~~~

当前实现中的 POST /v1/accounts/{account}/recharges/{order}/sandbox-settle 仅用于显式沙盒测试。它会走账务恢复路径，但不代表真实支付渠道已到账，不能由产品在正式支付流程中自动调用。若真实支付渠道未配置，生产请求会返回 payment_not_configured 或 503。

### 6.4 统一钱包向产品钱包划拨

统一钱包划拨是单独的资金操作，不会因为产品拥有 recharges:write 自动获得：

~~~http
POST /v1/accounts/{account}/transfers
Authorization: Bearer <access-token>
Idempotency-Key: transfer-account-20261103-0001
Content-Type: application/json

{"product_id":"notes","amount":"80000000"}
~~~

这只允许从同一账户的统一钱包向获准产品钱包划拨，不会跨账户、跨产品或把额度兑换成现金。每个 transfer 也必须按 operations 查询最终状态。

## 7. 订阅、套餐和加量包

充值是现金余额，购买套餐是商业订单；两者不能混成一个“余额增加”动作。

### 7.1 查询目录和购买

~~~ts
const offers = await hub.catalog();
const order = await hub.purchase(accountId, offers[0].id, 'order-account-20261103-0001');
~~~

等价 HTTP 请求：

~~~http
GET /v1/catalog
Authorization: Bearer <access-token>

POST /v1/accounts/{account}/orders
Authorization: Bearer <access-token>
Idempotency-Key: order-account-20261103-0001
Content-Type: application/json

{"offer_id":"offer-id"}
~~~

购买通常返回 202，需要分别读取订单的 payment_status 和 fulfillment_status。支付已完成但权益仍为 pending 时不能再次扣款。产品 token 只能看到自己 registry billing_products 获准的商品。

### 7.2 订阅升级和退款

~~~ts
const quote = await hub.quote(accountId, subscriptionId, offerId);
const upgraded = await hub.acceptQuote(accountId, quote.id, 'upgrade-account-20261103-0001');
const refund = await hub.refund(accountId, orderId, 'refund-account-20261103-0001');
~~~

升级报价绑定订阅版本和当前账期，过期或版本变化需要重新读取。退款不是任意金额强制扣回：未消费、没有在途预占且符合来源快照的部分才可能自动处理，其余进入 refund_requires_review/人工核实。退款请求也必须保留原幂等键并读取原订单。

### 7.3 计费预设

产品通过 PRODUCT_BILLING_FILE 或独立运营接口配置自己的计费策略。产品客户端只能读取，不能写价格或扩大可销售产品。

~~~ts
const presets = await hub.billingPresets();
const profile = await hub.billingProfile('notes');
~~~

仓库提供三种组合预设：

| 预设 | 订阅 | 额度 | 现金按量 | 适合场景 |
| --- | --- | --- | --- | --- |
| hybrid | 开启 | 月度额度、加量包可开 | 可选 | 会员 + 额度 + 预付费混合 |
| prepaid | 关闭 | 关闭 | prepaid_cash | 只按产品钱包现金消费 |
| subscription | 开启 | monthly_credit=0 | 关闭 | 只销售期限和功能权益 |

典型配置：

~~~json
[
  {
    "product_id": "notes",
    "version": 1,
    "subscription": {"enabled": true},
    "included_credits": {"enabled": true},
    "addons": {"enabled": true},
    "usage": {
      "enabled": true,
      "require_subscription": true,
      "funding_modes": ["credits", "prepaid_cash"],
      "default_funding_mode": "credits"
    }
  }
]
~~~

约束：

- included_credits 需要同时开启订阅和 credits 资金模式；
- addons 需要允许 credits；
- require_subscription=true 需要开启订阅；
- usage.enabled=false 时必须没有 funding mode，且不能要求订阅；
- 计费配置版本必须递增，同一版本内容不能修改；
- 已付款订单、已发放权益和在途调用保留创建时的条款快照。

准备和检查配置：

~~~sh
python3 scripts/product_billing.py prepare \
  --product-id notes --preset hybrid --version 1 \
  --output .local/notes-billing.json

python3 scripts/product_billing.py validate \
  --input .local/notes-billing.json
~~~

将审查后的绝对路径配置为 PRODUCT_BILLING_FILE，再按部署流程加载。计费配置不是身份配置，不能把密码或支付密钥放入其中。

## 8. 模型和按量计费

这一部分主要面向 LLMRouter 或接入 LLMRouter 的产品。普通三方产品可以使用订阅、额度和钱包接口，不会自动获得 LLMRouter 的内部结算权限。

### 8.1 签发短期模型凭证

产品后端向当前账户请求短期模型调用凭证：

~~~http
POST /v1/accounts/{account}/model-credentials
Authorization: Bearer <access-token>
Content-Type: application/json

{"models":["gpt-example"],"task_id":"task-20261103-0001","funding_mode":"credits"}
~~~

响应中的 token：

- 只允许指定模型和账户；
- audience 固定为 llmrouter；
- 最长约 10 分钟，并且不超过订阅有效期；
- 不是登录 token，也不是结算凭证；
- 只返回一次，不能依赖后续 API 恢复明文。

产品不能让浏览器直接持有该凭证。调用完成后由受信任的 LLMRouter 服务使用独立 MODEL_SERVICE_TOKEN 调用内部接口完成预占、补足、结算或释放。

### 8.2 预占、结算和释放

受信任模型服务的典型顺序：

1. GET /internal/v1/rates 读取已发布费率；
2. POST /internal/v1/model-credentials/verify 验证凭证和模型；
3. POST /internal/v1/calls/reserve 预占额度或现金；
4. 预占成功后才请求上游模型；
5. 成功得到可信 usage 后 POST /internal/v1/calls/{call}/settle；
6. 明确没有有效输出的失败调用调用 release；
7. 结果不确定时按原 call_id 查询，不重新请求上游。

call_id 是一次逻辑调用的稳定标识。供应商重试必须复用同一个 call_id 和 request_digest；用户有意发起下一次调用必须生成新 ID。结算重复提交必须幂等，改变原 usage 或 provider_cost 会产生冲突。

### 8.3 两种 usage 契约

旧版 exclusive_v1 使用 input、output、cache_read、cache_write、reasoning 五个整数计数；这些字段互不包含。新版 metered_v2 使用显式 measurement，例如 token、character、audio_second、image 或 provider_unit。两种契约不能混合。

~~~json
{
  "version": "metered_v2",
  "operation": "transcription",
  "state": "confirmed",
  "source": "provider",
  "measurements": [
    {"key":"audio","unit":"audio_second","quantity":{"value":"1001","scale":3}}
  ]
}
~~~

UniHub 根据冻结的 rate_id 精确计算，最终在资产边界统一向上取整。供应商成本 provider_cost 是独立观测值，不能代替用户计费金额。缺失 usage 不应被估算为零，会进入 MISSING_USAGE/待核实状态。

### 8.4 长期 API Key 和预算

需要用户在产品中长期调用时，可使用产品范围的 API Key：

~~~ts
const grant = await hub.createAPIKey(accountId, {
  name: 'server-worker',
  models: ['gpt-example'],
  funding_mode: 'prepaid_cash',
  max_per_call: '1000000',
  max_total: '100000000',
});
// grant.token 只在创建响应出现一次，交给受信后端保存。
~~~

Key 会绑定当前用户、账户、产品、实例、模型和资产，服务端只保存摘要。撤销、用户停用、产品撤权或 Key 过期会阻止新的预占；已接受的在途调用仍按原付款方完成结算或释放。

预算是独立于钱包余额的消费上限：

~~~ts
const budget = await hub.usageBudget(accountId, {
  product_id: 'llmrouter',
  instance_id: 'default',
  funding_mode: 'prepaid_cash',
});
~~~

预算更新使用 version 比较交换。held 是尚未完成调用的预占，used 是已结算消费，remaining 是预算扣除两者后的剩余；空上限表示不限，0 表示禁止新的预占。预算不是充值，也不会生成资金。

## 9. 运营配置和产品权限的边界

三方产品可以读取自身目录、计费策略、订阅、额度和账务状态，但不能自行：

- 发布或修改价格、商品、费率和计费配置；
- 修改其他产品的权限、账户成员或平台管理员；
- 直接写 Formance 账本或修改余额；
- 调用 /internal/v1/* 结算接口；
- 读取供应商密钥、模型服务 token 或其他产品的私有数据。

商品、费率、退款人工复核和计费策略写入使用独立的 operator audience 与 BILLING_OPERATOR_IDS 白名单。运营 token 只放在受信服务端，不能放进产品前端、URL、配置仓库或日志。

## 10. 幂等、错误和恢复规则

### 10.1 幂等键

所有充值、购买、升级接受、退款、转账和结算命令都要使用稳定的 Idempotency-Key。网络失败或结果未知时：

1. 保留原 key；
2. 读取原订单、充值单或 operation；
3. 只在确认没有原命令后再决定下一步；
4. 不能换一个 key 重新扣款、重新创建订单或再次请求供应商。

幂等键重复但参数不同会返回 409 conflict。不要用用户输入直接作为长期 key；建议包含业务对象、操作类型和随机唯一值，并在重试期间持久保存。

### 10.2 常见错误

| HTTP | 错误 | 处理 |
| --- | --- | --- |
| 400 | invalid_request | 修正参数，不重试原命令 |
| 401 | unauthorized | 重新完成登录或刷新受保护会话 |
| 402 | insufficient_funds / insufficient_credits | 展示余额/额度不足，不能自动买包 |
| 403 | forbidden | 重新读取产品授权和账户成员关系 |
| 404 | not_found | 确认对象属于当前账户和产品 |
| 409 | conflict / operation_pending / refund_requires_review | 先读取原对象、版本或退款状态 |
| 429 | rate_limited | 遵守 Retry-After，不能立即重放密码/注册操作 |
| 503 | payment_not_configured / service_unavailable | 标记能力不可用；不要把未知支付结果显示为失败后再重付 |

202 只表示命令已进入异步或待核实流程，不代表支付成功。资金操作只有账本 APPLIED 才能在产品界面显示为已到账。

### 11.1 已有系统

1. 盘点原系统的用户 ID、认证器、Key、余额、订单、调用和退款状态。
2. 注册独立 UniHub client，确定精确 callback、Origin、product、instance 和 scopes。
3. 建立 `(issuer, subject) -> local_user_id` 的唯一映射表。
4. 让产品后端在登录后调用 /v1/me 和 /v1/me/product-access。
5. 将产品页面的会员、额度、充值和订单读取切换为 UniHub API；保留原产品资源和页面结构。
6. 选择 prepaid、hybrid 或 subscription 计费策略，并由运营配置 PRODUCT_BILLING_FILE。
7. 对充值、购买、额度发放和模型调用分别做幂等和对账。
8. 单独审查旧余额导入；身份映射完成不等于资金导入完成。

### 11.2 新建系统

1. 使用 product_user_init.py 从已审查的 UniHub checkout 生成产品骨架。
2. 实现产品自己的 SQL 用户表、资源初始化和 Provisioner 事务。
3. 使用 issuer + subject 唯一键，不能用 email 作为主键。
4. 只在后端保存 token 和 service credential；浏览器只拿产品会话。
5. 通过 /v1/accounts、/v1/catalog、/v1/accounts/{account}/resources 建立产品页面。
6. 需要收费时再申请最小的财务 scopes，并接入充值、订单、操作查询和预算。
7. 先完成沙盒闭环，再评估真实支付和生产迁移，不要把测试商品/额度当成正式商业承诺。

生成示例：

~~~sh
python3 scripts/product_user_init.py --output /tmp/my-notes --product-id notes --module example.test/my-notes
cd /tmp/my-notes
go test ./...
cp config.example.json config.json
chmod 600 config.json
PRODUCT_CONFIG=./config.json go run .
~~~

### 11.3 LLMRouter/模型系统

1. 注册 llmrouter 产品和明确的 instance_id。
2. 由产品后端为账户签发短期模型凭证或长期产品 API Key。
3. LLMRouter 使用独立服务 token 验证凭证、预占、补足、结算和释放。
4. 上游请求失败、超时或结果未知时按原 call_id 对账，不能重新发起供应商调用。
5. 查询 /model-pool/calls、/model-pool/usage/summary 或产品范围的 calls/usage 时，区分 CREDIT/6 和 CNY/6。

其他三方产品可以复用身份和账户协议，但不会自动获得 LLMRouter 内部计费适配器。若要计量新的服务类型，必须定义自己的资产、单位、费率和受信结算适配，并单独验收。

### 11.4 使用平台后台接入工作台

平台管理员可以在“系统维护 → 三方接入”生成接入计划。页面要求填写产品标识和精确回调地址，并用复选框选择最小 scopes；`openid` 必须保留，`profile`、`email` 和 `billing:read` 按实际需要增加。页面还会把 hybrid、prepaid、subscription 计费组合和 sandbox 标记写入一条可复制的 `unihubctl integration prepare` 命令。

该工作台是参数和读回清单，不会在浏览器登记客户端密钥，也不会替服务端完成 OIDC 密钥保存、回调校验、支付渠道配置或账务发布。接入方需要在服务端执行命令或等价流程，然后读回 capability、产品、方案、用户映射和账单状态。支付渠道未配置时，控制台仍可浏览方案和用量，但购买和充值按钮必须保持不可用或明确标记为沙盒操作。

对于新建系统，推荐顺序是：先完成身份登录和 issuer+subject 映射，再读回账户与产品授权；随后由管理员发布 BillingProfile 与商品草稿，最后接入购买、充值、账单查询和 UNKNOWN 状态恢复。接入方不得根据浏览器展示推断到账，必须以订单履约和账本 `APPLIED` 读回为准。

## 12. 接入验收清单

### 注册与身份

- [ ] 独立的 product/client/application 已登记并完成 readback；
- [ ] callback 和 Origin 都是精确 HTTPS 地址；
- [ ] 浏览器流程使用 PKCE、state、nonce 和服务端会话；
- [ ] 产品用户映射以 issuer+subject 为唯一键；
- [ ] 并发首次登录不会创建多个本地用户；
- [ ] 未把 token、密码、refresh token 或 service credential 放到浏览器或日志。

### 账户和充值

- [ ] /v1/me、/v1/accounts 和产品授权读回通过；
- [ ] owner/billing_admin 与普通 member 的财务权限分离；
- [ ] 金额全部使用规范整数串；
- [ ] 充值、转账和订单命令都有持久化幂等键；
- [ ] 以 APPLIED 为到账条件；
- [ ] UNKNOWN、operation_pending 和网络超时都能按原对象恢复；
- [ ] 沙盒结算没有被误标成真实支付。

### 计费和模型调用

- [ ] billing profile、Offer、Rate 和 product registry 的版本关系已审查；
- [ ] 会员、额度、预付现金和退款状态分别展示；
- [ ] 模型预占成功后才请求上游；
- [ ] 结算重复提交不会重复扣款；
- [ ] 缺失 usage 不会被估算为零；
- [ ] CNY/6 和 CREDIT/6 不会相加或隐式转换；
- [ ] 长期 Key 只在创建响应返回明文，并有撤销和预算策略。

### 当前实现边界

- [ ] 当前真实支付渠道是否已配置，应以 /v1/capabilities 的 real_payments_configured 和实际部署验收为准；
- [ ] sandbox-settle 仅用于测试，不代表支付机构已完成付款；
- [ ] packages/sdk 尚未发布到 npm，接入方需锁定源码版本或内部镜像；
- [ ] packages/userkit-go 尚未发布公共 Go 版本，需使用审查过的源码快照；
- [ ] 生产 OIDC、真实支付、供应商结算和历史余额迁移都要有独立的部署读回和业务验收证据。

## 13. 相关文档

- [Headless 产品 API](./headless-product-api.md)
- [账户与资金 API](./account-wallet-api.md)
- [订阅、模型额度与用户中心 API](./subscriptions-credits-api.md)
- [可组合计费](./composable-billing.md)
- [产品用户基础接入](./integrations/product-user-foundation.md)
- [userkit-go 运行时适配器](./userkit/runtime-adapter.md)
- [SDK README](../packages/sdk/README.md)
- [OpenAPI 机器契约](../api/openapi.yaml)
