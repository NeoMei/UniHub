# 可组合产品收费接入

本页描述当前工作区的可组合收费契约；不代表公网部署、正式价格、真实支付或真实模型供应商已验收。机器契约见 [OpenAPI](../api/openapi.yaml)，产品 SDK 仍是本地 private 包，未发布到包仓库。

收费配置属于产品 `product_id`，同一产品所有 OIDC 客户端共享一份版本化 `BillingProfile`。身份客户端的 scopes、audience、redirect、Origin 与账费产品白名单仍由 `PRODUCT_CLIENTS_FILE` 管理；收费方式使用独立的 `PRODUCT_BILLING_FILE`，不复制身份配置或管理秘密。价格、月/年周期、等级、权益和赠送数量保留在不可变 Offer 条款中，配置模板本身不会发布商品或费率。

## 模板与组件

| 预设 | 订阅 | 周期额度 | 加量包 | 按量资金模式 | 会员门槛 |
| --- | --- | --- | --- | --- | --- |
| `hybrid` | 开启 | 开启 | 开启 | `credits` | 需要 |
| `prepaid` | 关闭 | 关闭 | 关闭 | `prepaid_cash` | 不需要 |
| `subscription` | 开启 | 关闭 | 关闭 | 关闭 | 不提供模型调用 |

未配置的产品保留版本 `0` 的既有 hybrid 行为。hybrid 年付仍逐月发放额度，用完拒绝调用。prepaid 直接消耗付费账户的 **llmrouter 产品钱包** `CNY/6` 充值余额，不透支，不扣来源产品钱包或统一钱包。subscription 只销售月/年有效期及功能权益；会员 Offer 的 `monthly_credit` 必须是 `"0"`，不生成额度批次、零数量账务命令或无限模型权限。纯订阅退款需要人工核实，不能按零消费额度自动全退。

预设是组合的快捷写法。完整配置还可表达“订阅 + 现金按量”或同时允许两种资产，见 [subscription-cash.json](../examples/billing/subscription-cash.json) 与 [dual-assets.json](../examples/billing/dual-assets.json)。组件约束：

- `included_credits.enabled` 要求订阅开启，并允许 `credits`；`addons.enabled` 要求允许 `credits`。
- `usage.enabled=true` 要求至少一个不重复的 `funding_modes`，且 `default_funding_mode` 必须在其中。`require_subscription=true` 要求订阅开启。
- `usage.enabled=false` 时，`funding_modes=[]`、`default_funding_mode=""`、`require_subscription=false`。

一次调用只用一种资产。允许双模式不表示额度耗尽会扣现金；产品须显式选择获准的 `funding_mode`，省略时只使用配置的默认模式。`CREDIT/6` 和 `CNY/6` 分别发布费率，历史空资产费率按 `CREDIT/6` 解释。不得把额度费率当现金费率。

## 部署配置与离线工具

配置是最多 100 个产品的严格 JSON 数组，不允许未知字段、重复产品、预设与组件混写。预设条目例如：

```json
[{"product_id":"my-product","version":1,"preset":"prepaid"}]
```

自定义条目须同时提供 `subscription`、`included_credits`、`addons`、`usage` 四个完整组件。各文件可从 [examples/billing](../examples/billing) 复制；示例 `my-product` 须替换为已登记且启用的产品。配置不会修改身份服务。所有文件仅含公开商业元数据，不应包含密码、token、client secret 或支付/模型密钥。

标准库工具只在本地生成或校验文件，不联网、不读取管理环境、不应用运营写入。输出文件必须不存在，防止覆盖已审查配置：

```sh
python3 scripts/product_billing.py prepare \
  --product-id my-product --preset prepaid --version 1 \
  --output .local/my-product-billing.json
python3 scripts/product_billing.py prepare \
  --input examples/billing/subscription-cash.json \
  --output .local/subscription-cash-billing.json
python3 scripts/product_billing.py validate --input .local/my-product-billing.json
```

准备输出目录后执行上述命令。审查配置并通过部署环境设置 `PRODUCT_BILLING_FILE` 为该文件的绝对路径，再按正常部署流程启动服务。启动配置版本从 `1` 开始，必须单调增加；同版本必须与已保存内容完全一致，较旧版本或同版本改写被拒绝。一个文件的配置原子应用，不能用降低版本来回滚。变更只影响新的销售/模型预占；已付款订单、既有权益、在途调用和 UNKNOWN 恢复保留原始条款及账务载荷。

## API 与 SDK

| 路径 | 权限与行为 |
| --- | --- |
| `GET /v1/billing-presets` | 产品 `catalog:read`；返回 `hybrid`/`prepaid`/`subscription` 对象，模板 `product_id="template"`、`version=0` |
| `GET /v1/products/{product}/billing-profile` | 产品 `catalog:read`；只读来源或显式获准的账费产品；控制台按其独立身份边界读取 |
| `GET /v1/products` | 目录行仍有 `id`/`name`，账费服务启用时可附加 `billing_profile` |
| `GET /v1/accounts/{account}/resources` | 产品响应可附加来源产品 `billing_profile`；控制台可能省略此字段，可通过目录读取其他产品 |
| `GET /ops/v1/products/{product}/billing-profile` | 独立运营 Bearer audience 的只读接口 |
| `PUT /ops/v1/products/{product}/billing-profile` | 独立运营 Bearer audience，且操作者在配置的运营白名单；请求完整 profile，body `version` 是预期当前版本，成功追加并返回 `version+1` |

PUT 的 `product_id` 必须与路径一致，版本过期返回 `409`。运营路径不接受产品 token；产品不能更新配置、发布价格或扩大权限。运营凭据只放在受信后端进程环境或秘密存储中，不能放入产品 UI、配置文件、URL 或日志。未知写入结果先 GET 读取当前版本并比较，禁止自动重发覆盖。

```ts
import { UniHubClient } from './packages/sdk/src/index.ts';
const hub = new UniHubClient({
  baseUrl: 'https://hub.example.com',
  clientId: 'my-product-web',
  redirectUri: 'https://app.example.com/login/callback',
});
hub.setAccessToken(productAccessToken);
const presets = await hub.billingPresets();
const profile = await hub.billingProfile('my-product');
const products = await hub.products(); // row.billing_profile 可选
const resources = await hub.resources(accountId); // 来源 profile 可选
// 保持旧三参数调用：省略 funding_mode，服务端使用配置默认值。
const defaultGrant = await hub.issueModelCredential(accountId, ['model'], taskId);
// 只有 profile 允许且有对应资产已发布费率时才可签发。
const cashGrant = await hub.issueModelCredential(accountId, ['model'], taskId, 'prepaid_cash');
// grant 固化 funding_mode、asset、billing_profile_version，交给 llmrouter。
```

SDK 只暴露产品读取与既有财务命令，不持有运营权限、不内置配置写入。签发及新预占重新检查成员关系、当前产品政策、可选会员门槛和对应资产费率；历史未标资金模式的凭证按 credits 处理。来源产品不指定扣款钱包，llmrouter 依据可信凭证选择费率并执行一次结算，不在本地重复扣款。

现金预付示例：经运营审批允许产品访问 `llmrouter`（`billing_products` 与对应 recharge scopes）后，为其充值；创建单不表示到账：

```ts
const recharge = await hub.createRecharge(accountId, 'llmrouter', '10000000', savedKey);
const current = await hub.getRecharge(accountId, recharge.id);
// 真实支付尚未配置时返回 payment_not_configured；沙盒结算须显式测试调用。
```

现金充值要求整分，`"10000000"` 为 10 元；模型消费可到微元。所有金额、计数及 usage 都保持规范十进制整数字符串，SDK 不用 `Number` 转换。资金不足拒绝；预占失败释放、未用预占回原钱包，未知计量/网络结果继续查询和恢复原操作，不换 key 或资产重扣。统一钱包划拨、账户财务角色及授权上限不因 prepaid 放宽。

## 查询资产与兼容

`calls(account,{asset:'CNY/6'})` 与 `usage(account,{asset:'CREDIT/6'})` 接受可选资产筛选；省略包含两种资产。`CallView.asset` 返回调用真实资产。财务角色才有 `held`/`charged`；普通成员不会收到假零。

- 单资产汇总保留 `asset`/`charged`，并提供该资产的 `charges_by_asset`。
- 混合汇总返回 `asset:"mixed"`，**省略 `charged`**，例如 `charges_by_asset:{"CREDIT/6":"42","CNY/6":"17"}`。两种数值不能相加。
- 非财务角色仍可获得 `asset` 标识，但 `charged` 和 `charges_by_asset` 均省略。
- 只累计已结算消费，未知/缺失 usage 单列；没有匹配调用时返回所筛选资产的零，未筛选默认 `CREDIT/6`。

游标绑定账户、当前 actor、筛选（含 asset）与位置。后续页使用同一资产筛选与 `next_cursor`；省略时间范围可由游标恢复，其他范围规则与 [Headless API](headless-product-api.md) 一致。每次请求重新检查权限，独立请求是 live 查询快照。

```sh
npm --prefix packages/sdk test
npm --prefix packages/sdk run typecheck
python3 scripts/test-product-billing.py
```

SDK 测试通过真实本机 HTTP server 核对请求路径、Bearer、显式资金模式、旧签名、未知/禁止路由错误、双资产查询和大整数串；离线工具测试校验生成、组合约束、拒绝未知字段和拒绝覆盖。它们不替代真实 PostgreSQL/Formance 闭环、部署读回或支付供应商验收。

完整实现及测试范围见[本轮验收](composable-billing-validation-2026-10-25.md)。
