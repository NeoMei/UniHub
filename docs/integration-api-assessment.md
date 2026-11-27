# UniHub 后台模块 API 评估

2026-11-26 在 113 主机核对新部署。LLMRouter 为 `v1.0.0-rc.51+quukk.llmrouter.e62d834e9`，Logto 为 `1.46.0`，Lago 为 `v1.57.0`。健康与发现接口返回 200。Logto 应用和组织管理、Lago 客户和套餐、新 LLMRouter 模型与渠道接口响应未认证请求为 401。授权读写及模型扣费端到端待联调。

## 后台模块接口

| 模块 | 可用接口 | 整合要补的工作 |
| --- | --- | --- |
| Logto | OIDC 发现、认证、令牌和退出。内网 Management API 管应用、用户、组织及成员，另有事件 Webhook。 | 两区各自部署一套 OSS，各自设置 `LOGTO_ISSUER`、客户端及回调。UniHub 管会话、企业付款权限、应用订阅席位及成员用量。 |
| Lago | 客户、套餐、订阅、附加费用、用量事件、账单及 PDF。 | UniHub 写客户和账单同步适配器。现有实例的许可证环境变量为空。动态覆盖每个订阅的席位数量和录入手工收款明细在源码中受 Premium 限制。由 UniHub 计算席位、续费与增购订单，验证现有一次性出账、跳过自动收款和更新已结算状态的组合。 |
| 新 LLMRouter | `/v1/models`、推理及任务路由、内网渠道管理。计费有 `FundingSource` 和异步任务差额结算。 | 增加管理统一 Key 的内网接口、可路由模型与计量元数据只读接口、按官方用量和倍率计算的第一层报价，以及外部账本和异步恢复。对外 `/v1/models` 改为使用 UniHub 过滤结果。 |

Lago 作为按需出账候选。`POST /api/v1/invoices` 接受 `fees[].units`、`fees[].unit_amount_cents` 和 `skip_psp`，适合由 UniHub 传入已确定的席位和金额。`PUT /api/v1/invoices/{id}` 可更新已定稿账单的 `payment_status`。实收金额和收退款流水仍以 UniHub 账本为准。接入前用未配置 `LAGO_LICENSE` 的环境生成账单样张，验证行项目、金额、账单状态和重复同步。[Lago 定价](https://getlago.com/pricing) · [账单源码](https://github.com/getlago/lago-api/blob/v1.57.0/app/controllers/api/v1/invoices_controller.rb)

## 计费协议与责任归属

品牌反代将 `/v1/models`、模型调用和异步任务交给 LLMRouter。UniHub 门户负责 Key 管理交互，网关仅接受可信内网请求来签发、轮换或吊销统一模型 Key。LLMRouter 持有密钥摘要和绑定数据，为同一 Key 解析应用、操作者、付款账户和地区。面向客户只暴露 UniHub 品牌入口。

**目录同源。** LLMRouter 提供只读可路由清单，用网关对外模型名作稳定 `model_id`，渠道、上游模型版本、能力 `operation` 和计量方式由独立 `route_id` 区分。UniHub 校验路由能力后发布目录与售价版本。门户与 `/v1/models` 调用相同的 UniHub 目录解析，按可信 Key 或门户会话上下文的应用、付款账户类型、地区、当前权益及生效版本过滤，并与网关当前启用且可计量的路由取交集。下线模型退出目录，新预扣拒绝。在途已授权流式响应独立传输，UniHub 不可用时，新目录请求和预扣返回可重试错误。

内部接口首版采用 `/internal/billing/v1/`。网关通过独立服务凭证调用 UniHub。UniHub 验证服务身份、`region + currency` 和 `gateway_version`，核对网关 Key 元数据、原预扣以及该用户在应用下的席位和付款权限。应用请求不能自行指定记账身份。两层计价使用以下拟议字段。

| 数据 | 归属和用途 |
| --- | --- |
| `model_id`、`route_id`、`operation`、`meter_version`、`router_rate_version` | LLMRouter 按官方计费产品、版本、计量单元、单价与渠道或模型倍率出具报价。 |
| `items[]`、`provider_currency`、`official_amount`、`base_amount` | LLMRouter 报送各计费项的单位、数量、单价和金额，并区分官方口径金额与倍率后基础金额。预扣报送计费用量上限，结算报送实际用量及证据引用。原始币种单独表示。 |
| `catalog_version`、`retail_rate_version`、`fx_version`、`credit_conversion_version` | UniHub 按允许路由固定发布的 `meter_version` 和 `router_rate_version`，再应用本区换算与第二层溢价或折扣。网关变价需等对应 UniHub 售价版本生效。预扣校验版本与报价规则，并固化快照、积分与上限。 |

金额使用带币种与精度的十进制值传递。**[拟议默认]** 应扣积分以百万分之一积分作为账本最小单位。单个请求累计完计费分项后取整，预扣向上取整，结算四舍五入并受已授权预扣限制。区内无显式可审计换算规则时拒绝接收异币种报价。计量项使用版本化单位集，首发候选包括输入、输出与缓存 Token、图文模态输入 Token、生成图片、处理页数与调用次数。渠道规则明确各计费项与上限的映射及是否叠加，总量和分项只按官方口径计算一次。返回的数据含证据来源，缺失的用量走核查流程。

| 操作 | 责任和恢复方式 |
| --- | --- |
| 预扣与补占 | 按稳定 `request_id`、应用和付款账户申请预扣或补占。UniHub 原子检查企业池、成员周期余量与批次。返回 `hold_id`、费率版本、授权上限和 `expires_at`。请求参数不一致的重复编号进入冲突处理。 |
| 取得提交权 | 网关在外部请求前持久化尝试记录并取得 UniHub 的唯一提交权。**[拟议默认]** 按预扣和尝试序号授权同一执行者，只有已确认未受理或可安全幂等重试时才允许复用或开启下一次尝试。仅在用户已经接受的上限内自动切换到同一售价版本批准的路由，必要时先补占并固定该尝试的报价快照；对客结算保持唯一。 |
| 结算与释放 | 网关持久化上游引用、终态和待发送记录，经服务凭证提交 `hold_id`、`request_id`、计费尝试、实际用量与第一层金额。UniHub 按该已授权尝试的两层报价、原周期与上限结算。超出部分进平台成本差异。成员权限变化由新请求校验，原交易按快照完成。终态重复投递返回原结果。 |
| 预扣状态查询与到期巡检 | 用原请求号及任务引用核查。未提交、已确认无应扣用量时可释放。受理或用量未知进核查，到证据截止按已核实部分结算并释放余量，迟到证据进平台对账。 |

当前网关异步轮询存在独立的钱包或订阅调整逻辑，需单独改接 UniHub，不能只靠请求计费入口覆盖。滚动升级期间，兼容的旧网关完成已受理任务和待发送终态，新调用切到新版。验收同步、流式、图像与长时异步路由时，分别观察用量证据、重启恢复和延迟。

**[拟议默认]** 同步路由以官方返回和网关持久化的可信用量为证据，有上游查询能力再回查；异步路由优先用任务引用回查。确认不了的部分到证据截止释放并计平台对账。**[待产品确认]** 阶段 3 前确定低余额时默认上限裁剪方案，以及可重试错误、渠道切换、失败尝试成本归属与对客报价变化的处理。**[拟议默认]** 缺少可覆盖的授权上限时拒绝开始，失败尝试独立记平台成本，未知受理进入核查。

## 多路由计量核验

下表的渠道和模型为首发核验对象。源码证据来自 LLMRouter `e62d834e9`，官方规则与实际可上线能力分别核对。每区上线前锁定渠道区域、合同或公开计价表版本、模型 ID、计量字段、计费状态和可验证上限，并用真实请求与渠道回执或账单对齐。

| 能力与候选版本 | 已核对的官方口径 | 网关基线与剩余核验 |
| --- | --- | --- |
| 文本 embedding · 阿里百炼 `text-embedding-v4` | 同步向量按输入 Token。批量与实时版本分别核价。 [向量接口](https://help.aliyun.com/zh/model-studio/text-embedding-synchronous-api/) · [百炼计价](https://help.aliyun.com/zh/model-studio/model-pricing) | 源码有 `/v1/embeddings` 和百炼兼容路径。**[待联调核验]** 实际区域、批次用量、计费输入字段。 |
| rerank · 阿里百炼 `gte-rerank-v2` | 按官方返回的输入计费用量统计，不计排序结果条数。该接口列有 `usage.total_tokens`。 [重排序接口](https://help.aliyun.com/zh/model-studio/text-rerank-api) · [百炼计价](https://help.aliyun.com/zh/model-studio/model-pricing) | 源码有 `/v1/rerank` 和阿里原生适配。**[待联调核验]** 查询与文档一起计量、字段与账单对应。其他渠道可有不同计费单位。 |
| 豆包文生图 · 火山方舟 Seedream 4.0 | 首发按实际成功生成图片张数，组图按实际输出数量结算。其他版本的额外计费项独立登记。 [方舟计价](https://docs.volcengine.com/docs/ark/model-pricing) · [文生图 API](https://docs.volcengine.com/docs/ark/image-generation-api) | 源码有方舟 `/api/v3/images/generations` 和 Seedream 4.0 模型别名。**[待联调核验]** 本区可用的模型 ID、官方计费数量字段、组图和部分成功。 |
| 阿里 VL embedding · 百炼 `qwen3-vl-embedding` | 同一请求区分文本 Token 与图片/视频计费 Token。该型号用 `usage.input_tokens` 和 `usage.image_tokens`，总量用于校对。 [多模态向量接口](https://help.aliyun.com/zh/model-studio/multimodal-embedding-api-reference) · [百炼计价](https://help.aliyun.com/zh/model-studio/model-pricing) | 源码有原生多模态路径与分项提取，缺用量时会回落到输入估算。**[待联调核验]** 真实结算保留分项原值，估算仅用作预扣，缺字段进入核查。 |
| PaddleOCR · 自托管 PaddleOCR-VL | 开源引擎无按次厂商 API 价。**[拟议默认]** 以成功解析页数作为运营计量，内部基础单价按部署成本配置，图片按一页计。 [PaddleOCR-VL 服务文档](https://github.com/PaddlePaddle/PaddleOCR/blob/main/docs/version3.x/pipeline_usage/PaddleOCR-VL.md) | 基线未找到专用 PaddleOCR 路由。**[待源码/联调核验]** 选择自托管版本和可计量 API，验证 PDF 页数、部分成功、文件限制和任务恢复。 |
| PaddleOCR · 收费托管候选百度智能云文档解析（PaddleOCR-VL） | 该产品按官方页数规则计费。原价、资源包和后付费用相应合同口径。 [百度智能云 OCR 计费](https://ai.baidu.com/ai-doc/OCR/rmvt5uvkv) | **[待产品确认]** 是否选收费渠道。**[待源码/联调核验]** 选定 API 版本及可计费页数、区域、失败与重复回调。单独建渠道与计量版本。 |

固定版本源码可核对 [统一模型路由](llmrouter/router/relay-router.go) · [阿里路由适配](llmrouter/relay/channel/ali/adaptor.go) · [多模态用量提取](llmrouter/relay/channel/ali/embedding.go) · [火山方舟适配](llmrouter/relay/channel/volcengine/adaptor.go)。

UniHub 首期提供订单明细和收款凭证。需要 Lago 出账时，两个项目以独立进程通过 HTTP 对接，异步发送已确认订单或单独的后付费用量。Lago 返回账单编号和账单状态用于对账。出账请求超时后按业务订单关联号核实落单情况，避免产生重复账单。

## 建议实施顺序

1. 方案复审后与用户共创 UniHub UI，设计审核通过后启动编码。阶段一的身份与账户本地验证已有记录，真实 Logto 登录待联调。
2. 先发布版本化应用商品、目录和 UniHub 售价，再交付账本、席位、支付和 SuperWagie 首套配置，分别验收版本回读与订单价格快照。
3. 新 LLMRouter 接入统一 Key、同源目录、官方计量与倍率报价。实测上述路由、流式和异步结算、两层费率锁版及异常恢复。
4. 双区配置和上游升级演练。有明确的企业出账需求时，再用 Lago 开源版验证新购、增购和已付款订单样张。

上述内部协议、权限校验和异常恢复属于拟实现的接口。113 的健康和认证边界已按文首所述检查。Logto OSS 的具体操作上限、Lago 无商业许可证的实单样张、各模型渠道可执行的用量上限及网关兼容性仍须独立联调。[业务和账务状态](architecture.md) · [验收计划](superpowers/plans/2026-11-26-unihub-rebuild.md)

## 源码与文档

- [Logto 1.46.0 应用接口](https://github.com/logto-io/logto/blob/v1.46.0/packages/core/src/routes/applications/application.openapi.json) · [组织接口](https://github.com/logto-io/logto/blob/v1.46.0/packages/core/src/routes/organization/index.openapi.json)
- [Lago 1.57.0 API 路由](https://github.com/getlago/lago-api/blob/v1.57.0/config/routes/shared_api.rb) · [动态固定费用](https://github.com/getlago/lago-api/blob/v1.57.0/app/services/subscriptions/update_or_override_fixed_charge_service.rb) · [手工收款](https://github.com/getlago/lago-api/blob/v1.57.0/app/services/payments/manual_create_service.rb)
