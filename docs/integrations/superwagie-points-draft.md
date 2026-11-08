# SuperWagie 订阅与积分：草稿商品及接入边界

本文件与 `examples/billing/superwagie-points-offers.draft.json` 是待商业验证的个人版商品设计，未导入、未发布、未修改任何已有商品、费率、钱包或线上策略。`status=draft` 不会出现在销售目录。统一标识为 `entitlements.pricing_program=superwagie-ai-points-v1`，接入端可用它识别这批新方案，不能把历史商品也判定为已迁移。

| 草稿商品 | 价格 | 每期/一次额度 | 期限 |
| --- | ---: | ---: | --- |
| 标准版 | ¥39/月 | 1,000 积分/月 | 按订阅锚点逐月发放，本期额度到期失效 |
| 专业版 | ¥99/月 | 3,000 积分/月 | 按订阅锚点逐月发放，本期额度到期失效 |
| 小加量包 | ¥9.8 | 200 积分 | 购买后 12 个月 |
| 大加量包 | ¥98 | 2,000 积分 | 购买后 12 个月 |

显示尺度为 **1 积分 = 1 CREDIT = 1,000,000 个 CREDIT/6 微单位**。`price` 使用 CNY/6 微元；两种资产独立，不存在隐式人民币换积分。方案售价不能倒推供应商成本或证明盈利。草稿不包含模型费率、不承诺具体任务次数，不启用自动买包；退款暂保留人工审核语义。

## 2026-11-08 原生积分实现增量（未部署）

当前工作树已实现显式 `funding_mode=credits` 的原生 Key、`llmrouter_native_credit` 定价标识、版本化有理数换算、CREDIT 批次预占/结算/释放、预算与报表，以及与 LLMRouter 的协议能力协商。配置和兼容边界见 [原生 CREDIT 计量协议](native-credit-metering.md)。没有导入商品、配置生产换算率、转换旧余额或部署；旧服务缺少协议能力、换算率未配置时继续禁止新积分销售。正式启用以实际部署和商业配置为准。

## 正式销售配置与验收边界

1. 当前工作树已支持显式积分 Key、积分模型目录、预占、结算、释放、预算和用量报表；现金 Key 保留 `prepaid_cash/CNY/6`。生产环境必须同时部署兼容的 UniHub、LLMRouter 和 SuperWagie，并配置版本化积分换算率。`sales_readiness.ready=true` 或商品可发布不能替代真实模型消费验收。
2. 新方案销售受服务端 `native_credits.ready` 控制；未配置换算率、旧运行时或能力协商失败时保持不可买。已受理的付款、订单和退款恢复不受新销售开关阻断。
3. 积分换算率为明确、不可复用版本修改的有理数及累计向上舍入规则，冻结进每次调用快照。供应商人民币成本独立记录；本稿没有生成或启用生产换算率。
4. 微信 Native 与支付宝电脑网站支付直连接口已编码。`PAYMENT_MODE=direct` 与受保护商户文件启用对应渠道；未配置时返回 `payment_not_configured`。本地签名协议与账务测试不代表真实收款、退款、线上回调或桌面安装验收。

## 已确认的真实支付渠道

2026-11-08，用户确认采用 **微信支付 + 支付宝，自有商户直连官方接口**。接入设计使用微信 Native 扫码及支付宝电脑网站支付官方收银台；具体订单、回调、金额和退款边界见 [真实支付接入设计](superwagie-direct-payments.md)。支付适配器、订单、发放、退款恢复及客户端界面已经接通；商户密钥、生产配置与真实收付款联调尚未完成。前述正式销售配置和验收边界仍有效。

## 加量包会员门槛与历史兼容

本稿的订阅和加量包均属于 `superwagie`。新购买加量包时，只有所属产品存在显式 `BillingProfile.version > 0`，并且两个会员门槛 `usage.require_subscription`、`usage.require_subscription_for_credits` 中至少一个为 true，才要求当前付费账户拥有**同产品** `active` 且 `now < paid_through` 的订阅。其他产品或其他账户的会员不能借用。无效会员返回 HTTP 409 `subscription_required`，订单和扣款不会创建。校验在账户锁内执行，并在锁定产品策略后、创建新支付订单前复核。

尚未配置策略的历史 `version=0` 默认 hybrid 保留原来的加量包购买行为；两个会员门槛均为 false 的独立加量产品保持可买。没有按产品名称硬编码绕过。未来将某产品从 v0 改成显式会员策略会启用新购买门槛，必须在该产品的发布评审中明确；本次不改现有策略。已付款订单的原幂等键恢复优先读取原快照，不因后来会员到期而阻断履约或重复发放。

## SuperWagie 受信财务代理

以下路径使用前缀 `/internal/v1/product-finance`，与 `/v1` 原业务接口共用权限和账务实现。需要登记服务身份、同产品同实例的当前 actor 证明、registry scope、账户财务角色及商品所属产品白名单；任一缺失均不授予写权限。

| 路径（省略前缀） | body | registry scope | 服务能力 / 幂等 |
| --- | --- | --- | --- |
| `POST /accounts/{account}/orders` | `{offer_id}` | `orders:write` | `finance:write`；必须且只能有一个合法 `Idempotency-Key` |
| `POST /accounts/{account}/quotes` | `{subscription_id, offer_id}` | `orders:write` | `finance:write`；只报价，不扣款 |
| `POST /accounts/{account}/quotes/{quote}/accept` | `{}` | `orders:write` | `finance:write`；必须且只能有一个合法 `Idempotency-Key` |
| `GET /accounts/{account}/orders` | 无 | `orders:read` | `finance:read` |
| `GET /accounts/{account}/credits` | 无 | `credits:read` | `finance:read`；成员仍须属于当前账户 |

报价返回 `id/account_id/subscription_id/subscription_version/offer/amount/grant_amount/period_start/expires_at/created_at`；接受报价返回商业 `Order`。付款状态 `PAID` 和权益状态 `FULFILLED` 必须分别确认。外部支付另有持久化 `/checkouts` 集合与单笔查单、核实、关闭、退款接口，详见真实支付接入文档；旧钱包没有新增 `GET quotes/{id}` 或 `GET orders/{id}`。网络结果未知时保留原 mutation key，读订单列表或恢复原操作，不能另起一笔购买。升级只支持同付款周期升档；报价版本变化或跨期需重新确认。

只有 `409 quote_invalidated` 明确表示该新升级在创建订单和扣款前，因报价过期、账期变化或订阅版本变化被拒绝，可读取新报价并重新确认。已提交订单的同 key 恢复优先于报价时效检查；即使报价过期，也继续恢复原扣款和订单。`conflict`、`operation_pending`、网络 UNKNOWN 不是该保证，不能据此换 key 重扣。

旧钱包购买从商品所属产品现金钱包扣款；不足时只按已有明确 funding 授权使用统一钱包，不会直接扣其他产品的钱包。旧 `llmrouter` 模型现金、历史 Key、未决调用和现金费率不转换、不迁移，也不删除。前台可保留次级“原有模型余额”入口，不能把旧人民币直接计入积分总额。

## 安全积分明细

产品 `GET credits` 保留真实批次 `id/account_id/asset/pool/original/available/reserved/consumed/expired/withdrawn/starts_at/expires_at/created_at/frozen`。新增：

- 当前来源产品的 `base/upgrade` 批次：`kind=subscription`、`source_scope=product`、`product_id=当前产品`。
- 当前来源产品的 `addon` 批次：`kind=addon`、`source_scope=product`、`product_id=当前产品`。
- 其他产品来源：`kind=other`、`source_scope=shared`，不输出来源产品、订单、订阅和 source key。批次 opaque ID 沿用已有契约。

额度仍是同一付费账户的共享池，分类仅说明来源，不能标为 SuperWagie 专属。外部直付直接记入支付收款账，不会再次扣现金钱包。只把当前有效且未冻结的 `available` 计入可用额度；预占和冻结独立展示。`resources.credits` 继续返回 `spendable/reserved/frozen` 汇总。旧服务未返回新增分类时，客户端应显示为未分类共享额度，不猜测订阅或加量来源。

## 本地验证

新增回归覆盖无会员、过期、取消、其他产品会员、显式/历史策略、并发策略变更、已付款加量订单重放；代理覆盖原scope、服务读写能力、跨账户/产品、缺失/重复幂等键、钱包购买和升级重放、真实支付未配置边界；投影覆盖真实数量/期限及跨产品隐私。

账务用例需设置仅指向本地测试环境的 `UNIHUB_TEST_DATABASE_URL` 与 `UNIHUB_TEST_LEDGER_URL`。`testutil` 创建并清理独立 PostgreSQL schema，Formance 使用每用例独立测试 ledger。缺少依赖的跳过不能计为账务通过；沙盒用例也不证明真实商户、供应商成本或原生 CREDIT 调用已经验收。

## 当前本地组合验证

`TestCommerceSignedPaymentNativeCreditsAndOriginalRefund` 使用真实支付宝适配器、临时 RSA 签名、受控网关响应、本地 PostgreSQL/Formance，串联会员购买、重复通知、发放、原生积分预占/结算、加量包购买与独立查证退款。验证旧现金钱包不变、无重复订单/发放、已消耗会员进入人工审核。实际商户网络与模型供应商不在此用例中。
