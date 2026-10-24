# 订阅、模型额度与用户中心 API

此接口属于 UniHub 自有产品第一阶段。现金为 `CNY/6`，模型额度为 `CREDIT/6`，两者不同资产；所有数量均为十进制整数字符串。`1000000` 表示 1 元现金或 1 个显示额度单位，**不表示两类资产可以互相兑换**。

## 浏览器认证

Go 服务同时托管 `web/dist` 和 API。设置 `PUBLIC_URL` 为准确的公开 origin（本地为 `http://localhost:8088`），OIDC 应用登记 `${PUBLIC_URL}/auth/callback`。身份服务负责密码；UniHub 采用 Code + PKCE、state、nonce，并校验 ID/access token 的身份一致性。

| 路径 | 行为 |
| --- | --- |
| GET `/auth/login` | 创建一次性登录事务，跳转身份服务 |
| GET `/auth/callback` | 校验响应并创建不超过 1 小时的服务端会话 |
| GET `/auth/session` | `{authenticated,user?,csrf_token?,sandbox}`，不返回登录令牌 |
| POST `/auth/logout` | 撤销当前会话，返回 204 |

浏览器持有 HttpOnly、SameSite=Lax 的随机会话 Cookie，HTTPS/生产模式加 Secure。数据库仅存会话 ID 摘要、已验证身份和到期时间；不存 access/refresh/ID token。所有使用 Cookie 的写请求须同时提供精确同源 `Origin` 和 `X-CSRF-Token`。产品仍可用已有 Bearer API；显式但无效的 Bearer 不回退为浏览器身份。

## 产品客户端权限

`OIDC_PRODUCT_CLIENTS` 配置独立的 `product=audience`，每个 audience 唯一且与控制台、运营分离。API 校验产品 access token 后从服务端配置确定产品身份。只开放 GET `/v1/me`、`/v1/accounts`、`/v1/products`、`/v1/catalog`、账户 `/subscriptions` 与 `/credits`，以及 POST 账户 `/model-credentials`。签发请求的 `origin_product_id` 必须与配置产品一致。资金、订单、团队修改及运营接口不对产品客户端开放；这些操作通过 UniHub 控制台进行。模型令牌不是登录令牌。

## 用户商业接口

以下路径除目录外均以 `/v1/accounts/{account}` 为前缀。账户来自已验证成员关系，不能通过请求 JSON 改写。资金、订单和授权管理仅 owner/billing_admin；成员可查询订阅权益和额度。

| 方法与路径 | 请求 / 返回 |
| --- | --- |
| GET `/v1/catalog` | 已发布、当前环境可销售的 `Offer[]` |
| GET `/subscriptions` | `Subscription[]`，含 offer 快照、anchor、paid_through、version |
| GET `/credits` | `Batch[]`，来源订单/产品、原始、可用、冻结、消费、过期、撤回、有效期及 frozen |
| GET `/orders` | `Order[]`，付款与权益发放状态分别返回 |
| POST `/orders` | `{offer_id}` + `Idempotency-Key`，返回订单（202） |
| POST `/quotes` | `{subscription_id,offer_id}`，返回绑定订阅版本和当前账期的升级报价 |
| POST `/quotes/{quote}/accept` | `{}` + `Idempotency-Key`，按报价生成订单 |
| GET `/settings` | 统一钱包补款和自动续费各自的授权记录 |
| PUT `/settings` | `{product_id,purpose,enabled,version,max_per_operation,max_total,expires_at}` |
| POST `/orders/{order}/refund` | `{}` + `Idempotency-Key`；按订单快照规则进入退款或人工核实 |

授权 purpose 仅为 `funding` 或 `renewal`，分别授权统一钱包补款与自动续费。首次 version=0，更新提交已读取版本。累计 used 由后端计算，更新授权不会清零。额度不足不会自动购买加量包。

`Offer` 字段以 `internal/billing/types.go` 为准：id（不可变版本 ID）、product_id、name、account_kind（personal/team/both）、kind（membership/addon）、period（month/year）、price、monthly_credit、entitlements、rank、validity_months、upgrade_rule、refund_rule、status、sandbox。加量包的 monthly_credit 字段表示本次包数量；其有效期由 validity_months 独立配置。

同周期升级要求显式 `remaining_term_prorata_v1`：按剩余已付期限计算价格差、向上取到内部最小现金单位；不改原锚点和到期日。当前月增加额度等于新等级月额度减去本订阅本账期累计已发量，不根据剩余额度重新发整月额度。跨月/年转换暂未开放。

退款规则为空时不会默认全额退款；进入 `MANUAL_REVIEW`。显式 `unused_only_v1` 只允许未消费且无在途预占的来源额度全额撤回后退款。旧账期、升级后改变的订阅及已用/已预占额度进入人工核实。人工核实期间来源批次 frozen，不可新预占，已预占请求仍可结算。当前支持运营拒绝退款并解除来源冻结；不提供任意金额强制批准、部分退款或真实支付退款。

金额、额度及状态的 JSON 定义以 Go types 为机器契约。`available` 是账本数量，显示“可消费额度”还须检查 `!frozen && starts_at <= now && now < expires_at`；不能只把 available 相加。

## 模型凭证与 llmrouter 服务接口

用户调用 POST `/v1/accounts/{account}/model-credentials`，请求 `{origin_product_id,models,task_id}`。有效产品会员和当前成员关系均须成立；models 只能选已有发布费率的模型。响应一次性返回随机 `token` 与身份范围，audience 固定 `llmrouter`，有效期最多 10 分钟且不超过会员到期。数据库只存摘要。DELETE `/v1/accounts/{account}/model-credentials/{credential}` 可撤销，成员只能撤销自己签发的凭证，owner/billing_admin 可撤销该账户凭证。

`/internal/v1/*` 只接受独立 `MODEL_SERVICE_TOKEN`，普通用户令牌、浏览器 Cookie、模型凭证均不能代替该服务身份。部署时只给受信任 llmrouter 实例此凭据；生产网络应限制该接口来源。未配置服务凭据时拒绝访问。

| 路径 | 请求 / 行为 |
| --- | --- |
| GET `/internal/v1/rates` | 发布的不可变费率版本 |
| POST `/internal/v1/model-credentials/verify` | `{token,model}`，验证范围和当前权限，只返回声明 |
| POST `/internal/v1/calls/reserve` | `{credential,task_id,call_id,request_digest,rate_id,amount}`；actor/account/origin 全部由凭证派生 |
| GET `/internal/v1/calls/{call}` | 按原 logical call 查状态，供超时恢复 |
| POST `/internal/v1/calls/{call}/topup` | `{amount}` + `Idempotency-Key`，追加预占 |
| POST `/internal/v1/calls/{call}/settle` | `{usage,provider_cost?}`；可信最终用量，供应商成本可选，不能提交客户端自行决定的费用 |
| POST `/internal/v1/calls/{call}/release` | `{reason}`；明确无有效输出失败等受信任终态释放 |

`usage` 含 input/output/cache_read/cache_write/reasoning 五个整数字符串，`exclusive_v1` 约定各项互不包含。上游协议的包含关系由网关先规范化。UniHub 按 rate_id 锁定的五组 numerator/denominator 独立复算，分项精确有理数求和后只向上取整一次。`provider_cost` 是独立的可选观测，不作为用户额度扣费依据；省略或空字符串表示未知，保持为空（调用响应中省略该字段），不能用 `"0"` 代替未知。提供时须为 int64 范围内的规范非负整数字符串；`"0"` 表示已知零成本。最终结算的幂等摘要区分未知与零，已结算请求改变此值会返回冲突。`platform_difference` 仍表示按用量及锁定费率计算的额度超过预留上限的差额，与供应商成本无关；缺失供应商成本不阻碍已有可信用量的结算，缺失用量仍保留预留并进入 `MISSING_USAGE`。

call_id 是一次逻辑模型调用标识：SDK/供应商重试必须沿用，下一次工具循环或有意重复调用必须使用新 ID。request_digest 包含实际模型请求及绑定任务；同 ID 内容变化拒绝。不得仅把请求体 hash 当调用 ID。额度预占成功才可调用上游；结果不明通过原 call 查单，不能重新发上游。缺失 usage 保留待核实，超预占的最终差异独立记录，不能扣成负数。模型/成员/凭证撤销阻止新预占和追加；原在途请求可按原付款方完成结算。

## 运营接口

运营采用独立 `OPS_OIDC_AUDIENCE`（不可与普通应用相同），并在 `BILLING_OPERATOR_IDS` 显式列出 UniHub 用户 ID。需要两项都满足；普通用户中心会话或 owner 角色不授予运营权限。尚未配置时接口关闭。

| 路径 | 行为 |
| --- | --- |
| GET `/ops/v1/offers` | 含草稿的目录 |
| POST `/ops/v1/offers` | 创建/编辑草稿，发布后不可原地改价 |
| POST `/ops/v1/offers/{offer}/publish` | `{}`，发布版本 |
| POST `/ops/v1/rates` | 发布不可变有理数费率 |
| GET `/ops/v1/orders?account_id=...` | 运营查询，人工核实订单优先；可省账户参数 |
| POST `/ops/v1/accounts/{account}/orders/{order}/refund-review` | `{decision:"reject",reason:"..."}`，拒绝退款并解除来源冻结，保留审计 |

当前提供上述运营 API，独立运营可视化控制台仍待建设。长期账本 UNKNOWN 没有任意修改余额的结案接口，必须核对原始交易；不会为了让状态看起来完成而释放预占或重扣资金。

## 错误与恢复

400 invalid_request；401 未认证；403 forbidden/invalid_model_credential；402 insufficient_funds/insufficient_credits；409 conflict/operation_pending/unsupported_plan_change/refund_requires_review；503 service_unavailable。

收到 operation_pending 不应更换幂等键重试。订单可已付款但权益 pending；UI 必须展示两者，不再次要求付费。refund_requires_review 也可能已经冻结来源批次，须刷新订单/额度。钱包与商业命令共享账户锁及持久化队列屏障；重启 worker 会继续使用原始命令和引用恢复。

测试商品仅在 development 且 PAYMENT_MODE=sandbox 下可用，SANDBOX_CATALOG=true 才种入。真实商品默认草稿，真实支付渠道尚未接入，测试金额与测试额度不表示正式商业定价。
