# 归一（UniHub）阶段 2 完整技术设计方案（2a / 2b / 2c）

- **状态**：待评审确认（2026-11-28）
- **衔接基线**：
  - 架构实施方案：[architecture.md](./architecture.md) · [architecture-rationale.md](./architecture-rationale.md) · [2026-11-26-unihub-rebuild.md](./superpowers/plans/2026-11-26-unihub-rebuild.md)
  - 阶段 1 代码基线：`6b8483f`（Node `>=24` ESM、Fastify 5、`pg`、`migrations/001_identity.sql`、`node:test`）
  - UI 与品牌基线：[ui-design.md](./ui-design.md)（v5.1 全项拍板终稿）· [ui-prototype.html](./ui-prototype.html)

本文档将 **阶段 2a（商品/目录/售价发布与席位契约）**、**阶段 2b（钱包/支付/订单/席位/积分总账）** 与 **阶段 2c（SuperWagie 首套规则引擎与升级折算）** 拆解为可直接落码的数据库 Schema、事务算法、HTTP API 契约、前端工程结构与验收矩阵，供分 PR 实施。

---

## 1. 工程结构与 PR 交付拆分

延续阶段 1 轻量、零冗余、强 SQL 约束的工程风格，新模块按职责落入同一 UniHub 业务进程，前端采用 **React + Vite + Tailwind** 本地打包由 Fastify 静态托管：

```text
unihub/
├── migrations/
│   ├── 001_identity.sql              # 阶段 1 已有（region/users/oidc/accounts/members/sessions/apps）
│   ├── 002_commerce_catalog.sql      # 阶段 2a：商品草稿、不可变商品版本、模型路由售价版本、用户授权应用、席位契约
│   └── 003_billing_ledger.sql        # 阶段 2b/2c：钱包、支付单、现金流水、订单、outbox、订阅、席位、邀请、积分批次与流水
├── src/
│   ├── app.js / accounts.js / apps.js / identity.js / database.js / config.js
│   ├── commerce/                     # 阶段 2a
│   │   ├── drafts.js                 # 商品卡草稿 CRUD + 单卡独立上架/更新上架/下架 -> 固化不可变版本
│   │   ├── versions.js               # 不可变商品/售价版本发布、按时间解析与按 ID 回读
│   │   ├── routes-pricing.js         # LLMRouter 可路由目录校验、二层系数、最低粒度(>0)阻断校验
│   │   └── catalog.js                # 门户与 /v1/models 共用的同源目录解析服务
│   ├── payments/                     # 阶段 2b（继承原 NeoMei/UniHub 已验证支付与验签模块）
│   │   ├── wechat.js                 # 微信支付 v3（Native/H5/JSAPI + 回调验签）
│   │   ├── alipay.js                 # 支付宝 RSA2 证书模式（PC/WAP + 异步/同步验签）
│   │   ├── stripe.js                 # 美区 USD Stripe Checkout + Webhook 验签
│   │   └── index.js                  # deployment_region + currency 错配拒收与幂等分发
│   ├── billing/                      # 阶段 2b / 2c
│   │   ├── wallet.js                 # 现金钱包、充值入账、可原路退款额、欠款/拒付冻结、现金守恒
│   │   ├── orders.js                 # 统一结账、余额不足 pendingCheckout、成交快照、事务 outbox
│   │   ├── outbox-worker.js          # 待发放权益异步幂等恢复（按 order_id + cycle 去重）
│   │   ├── subscriptions.js          # 会员开通/续费、下周期降档/减席预约、自动续费与涨价重授权
│   │   ├── upgrade.js                # 个人立即升档/月转年折算引擎（基础积分公式 + 在途补返 + 连续升级）
│   │   ├── seats.js                  # 企业按应用分配席位（防超配并发锁）、增席补差、成员周期上限与邀请认领
│   │   ├── credits.js                # 百万分之一整数积分总账、(expires_at ASC, id ASC) 批次锁、守恒校验
│   │   └── reconciliation.js         # 每日现金/积分守恒巡检与差异报表
│   └── seed/                         # 阶段 2c
│       ├── superwagie.js             # SuperWagie 首套个人/企业商品与路由定价种子配置
│       └── docflow.js                # DocFlow AI 复用配置样例（验证零硬编码）
└── web/                              # React + Vite + Tailwind（本地打包到 dist/，零外链 CDN、严格 CSP）
```

### 三个独立可回滚 PR 的交付边界

| PR | 包含范围 | 独立验收门禁 |
| --- | --- | --- |
| **PR-2a** | `002_commerce_catalog.sql` + `src/commerce/` + 阶段 1 补齐的 3 个接口 + 运营后台「接入应用 / 商品卡 / 模型路由」前端页面 | 两应用发布个人/企业不可变版本；单卡上架/下架不泄漏其他草稿；按生效时间精确解析、按 ID 回读只读快照；正价归零阻断；同源目录按 `app × kind × region` 隔离。 |
| **PR-2b** | `003_billing_ledger.sql` + `src/payments/` + `src/billing/` 核心账本/钱包/订单/outbox/席位/邀请 + 门户与聚焦页前端 | 余额不足保留上下文跳充值再回跳；重复/乱序支付回调仅入账一次；扣款后进程崩溃重启由 outbox 幂等补发；并发分配席位不超配；撤 A 应用席位不影响 B；现金与积分 100% 守恒。 |
| **PR-2c** | `upgrade.js` 升级折算 + 增减席补差 + 下周期预约 + 自动续费策略 + SuperWagie / DocFlow 种子配置 | 全量覆盖月付/年付/月转年/升降档/取消自动续费/企业增减席/积分包到期/连续升级与在途补返 3 组数值用例；DocFlow 仅靠配置复用全部流程。 |

---

## 2. 阶段 2a 详细设计：商品、目录、售价版本与席位契约

### 2.1 数据库 Schema（`migrations/002_commerce_catalog.sql`）

严格遵循 `001_identity.sql` 的类型与强约束规范（`uuid` 主键、单行 `deployment_region` 外键/校验、`jsonb` 结构化快照、禁止修改已发布版本的触发器保护）：

```sql
-- 1. 应用元数据扩展与用户授权记录（补齐 UI 所需列表/简介/我授权过的应用）
ALTER TABLE apps
  ADD COLUMN IF NOT EXISTS description text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

CREATE TABLE user_app_authorizations (
  user_id uuid NOT NULL REFERENCES users(id),
  app_id uuid NOT NULL REFERENCES apps(id),
  first_authorized_at timestamptz NOT NULL DEFAULT now(),
  last_authorized_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz,
  PRIMARY KEY (user_id, app_id)
);

-- 2. 商品卡草稿表（支撑单卡独立编辑、按 rank 排序、单卡上架/更新上架/下架）
CREATE TABLE product_card_drafts (
  id uuid PRIMARY KEY,
  app_id uuid NOT NULL REFERENCES apps(id),
  account_kind text NOT NULL CHECK (account_kind IN ('personal', 'enterprise')),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  product_type text NOT NULL CHECK (product_type IN ('membership', 'credit_pack')),
  tier_id text NOT NULL CHECK (length(tier_id) BETWEEN 1 AND 64),
  tier_rank integer NOT NULL CHECK (tier_rank >= 0),
  publish_status text NOT NULL CHECK (publish_status IN ('new', 'published', 'modified', 'offline')),
  -- A 区：结构化可执行计费规则（供 src/billing/ 确定性执行）
  -- membership: { billingCycle, priceMinor, baseCredits, bonusCredits, carryoverPolicy, downgradePolicy, packEligibility, entitlementKeys }
  -- credit_pack: { priceMinor, packCredits, validDays, packEligibility }
  rule_json jsonb NOT NULL,
  -- B 区：面向用户的自然语言展示文案（仅展示不参算）
  -- { name, description, benefits: string[] }
  display_json jsonb NOT NULL,
  updated_by uuid NOT NULL REFERENCES users(id),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (app_id, account_kind, region, tier_id)
);

CREATE INDEX product_card_drafts_scope_idx
  ON product_card_drafts (app_id, account_kind, region, tier_rank ASC, id ASC);

-- 3. 不可变商品版本快照表（同一范围 + 生效时刻唯一；已落库禁止 UPDATE/DELETE）
CREATE TABLE commerce_versions (
  id uuid PRIMARY KEY,
  version_code text NOT NULL UNIQUE, -- 如 ver_sw_cn_p_03
  app_id uuid NOT NULL REFERENCES apps(id),
  account_kind text NOT NULL CHECK (account_kind IN ('personal', 'enterprise')),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  effective_at timestamptz NOT NULL,
  -- 仅包含本次发布生效的「已上架」商品卡完整快照数组（按 tier_rank ASC 固化）
  cards_snapshot jsonb NOT NULL CHECK (jsonb_typeof(cards_snapshot) = 'array'),
  change_summary text NOT NULL DEFAULT '',
  published_by uuid NOT NULL REFERENCES users(id),
  published_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (app_id, account_kind, region, effective_at)
);

CREATE INDEX commerce_versions_resolve_idx
  ON commerce_versions (app_id, account_kind, region, effective_at DESC);

-- 4. 不可变模型目录与二层售价版本表（按 应用 × 账户类型 × 地区 四维发布，不区分会员档位）
CREATE TABLE route_pricing_versions (
  id uuid PRIMARY KEY,
  version_code text NOT NULL UNIQUE, -- 如 rate_sw_cn_p_02
  app_id uuid NOT NULL REFERENCES apps(id),
  account_kind text NOT NULL CHECK (account_kind IN ('personal', 'enterprise')),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  effective_at timestamptz NOT NULL,
  credits_per_currency integer NOT NULL CHECK (credits_per_currency > 0), -- 如 CNY: 200, USD: 1400
  fx_version text NOT NULL DEFAULT 'fx_native_v1',
  -- 已启用允许路由快照：[{ modelId, routeId, operation, unit, minBillableQty, meterVersion, routerRateVersion, baseAmountDecimal, unihubMultiplier, entitlementFilter }]
  routes_snapshot jsonb NOT NULL CHECK (jsonb_typeof(routes_snapshot) = 'array'),
  published_by uuid NOT NULL REFERENCES users(id),
  published_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (app_id, account_kind, region, effective_at)
);

CREATE INDEX route_pricing_versions_resolve_idx
  ON route_pricing_versions (app_id, account_kind, region, effective_at DESC);

-- 5. 不可变版本写保护触发器（数据库层杜绝篡改历史快照）
CREATE OR REPLACE FUNCTION forbid_version_mutation() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'published version snapshot is immutable';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_commerce_versions_immutable
  BEFORE UPDATE OR DELETE ON commerce_versions
  FOR EACH ROW EXECUTE FUNCTION forbid_version_mutation();

CREATE TRIGGER trg_route_pricing_versions_immutable
  BEFORE UPDATE OR DELETE ON route_pricing_versions
  FOR EACH ROW EXECUTE FUNCTION forbid_version_mutation();

-- 6. 非密钥运行配置表（仅存放允许热更新的非敏感项：对客反代展示域名、Lago 开关、支付渠道启停）
-- 环境变量/密钥（UNIHUB_REGION, LOGTO_*, 支付私钥/证书）严禁入此表
CREATE TABLE runtime_settings (
  key text PRIMARY KEY,
  value_json jsonb NOT NULL,
  updated_by uuid REFERENCES users(id),
  updated_at timestamptz NOT NULL DEFAULT now()
);
```

### 2.2 单卡独立上架 / 更新上架 / 下架 → 不可变版本快照映射

满足 UI「每张卡片独立上架/下架，绝不误发其他草稿」与后端「版本不可变、按生效时刻唯一解析」的双重要求，核心事务如下：

1. **编辑草稿**：`PUT /api/admin/apps/:appId/product-drafts/:cardId` 仅写 `product_card_drafts`。若原 `publish_status = 'published'`，自动置为 `'modified'`；线上 `commerce_versions` 保持不变。
2. **单卡上架 / 更新上架**：`POST /api/admin/apps/:appId/product-drafts/:cardId/publish`（支持可选 `effectiveAt`，默认 `now()`）：
   - 在 `transaction` 内锁定该 `(app_id, account_kind, region)` 范围的 `product_card_drafts` 行（`FOR UPDATE`）；
   - 读取当前最新生效的 `commerce_versions.cards_snapshot`（若为首发则为空数组 `[]`）；
   - 从 `cards_snapshot` 中按 `cardId` 替换或插入**仅目标这一张卡**的最新 `rule_json + display_json` 快照，按 `(tier_rank ASC, cardId ASC)` 排序；
   - **其他 `publish_status IN ('new', 'modified', 'offline')` 的草稿完全不进入本次 `cards_snapshot`**；
   - 插入一条新的不可变 `commerce_versions` 记录，并将目标卡 `publish_status` 更新为 `'published'`；
   - 返回新生成的 `version` 与更新后的草稿列表。
3. **单卡下架**：`POST /api/admin/apps/:appId/product-drafts/:cardId/unpublish`：
   - 同事务读取当前最新生效的 `cards_snapshot`，仅移除目标 `cardId`；
   - 插入一条新的不可变 `commerce_versions`（新用户立即不可见/不可新购；历史订单仍按原 `commerce_version_id` 回读不受影响）；
   - 目标卡 `publish_status` 更新为 `'offline'`（保留全部规则与文案，随时可「重新上架」）。

### 2.3 模型路由与售价发布的最低计费粒度校验

发布 `route_pricing_versions` 前，对 `routes_snapshot` 中每条已启用路由执行确定性十进制定点校验（整数运算，避免 IEEE-754 浮点误差）：

```text
单次最小账本单位 = round(base_amount_in_region_currency × unihub_multiplier × credits_per_currency × min_billable_qty × 1_000_000)
```

- 约束：`unihub_multiplier > 0` 且 `单次最小账本单位 >= 1`（例如 `text-embedding-v4` 最小调用 `0.04 × 1K tokens` → `0.0040 积分 = 4,000` 账本单位 `> 0`）。
- 任一已启用路由不满足时返回 `400 { error: 'granularity_underflow', routeId }`，阻断发布。
- **全模型云 API 约束**：所有路由（含阿里百炼向量/rerank/VL、火山方舟 Seedream 4.0、百度智能云 PaddleOCR-VL 文档解析 API）均须绑定明确的外部渠道 `meter_version` 与 `router_rate_version`，无本地模型推理路径。

### 2.4 阶段 2a HTTP API 契约清单

所有写操作必须携带有效会话与 `X-CSRF-Token`；鉴权按 `isPlatformOperator`（平台管理员）与 `isAppOperator(appId)`（单应用运营）严格隔离：

| 分组 | 方法与路径 | 权限 | 关键请求/响应语义 |
| --- | --- | --- | --- |
| **Stage 1 补齐** | `GET /api/admin/apps` | 平台管理员 | 返回全部已登记应用列表（含回调数、运营人数、已发布版本数） |
| | `POST /api/admin/apps/:appId/rotate-secret` | 平台管理员 | 轮换 `client_secret_hash`，一次性返回明文 `clientSecret` |
| | `GET /api/me/authorized-apps` | 登录用户 | 返回当前用户「我授权过的应用」列表（首次/最近授权时间） |
| | `DELETE /api/me/authorized-apps/:appId` | 登录用户 | 撤销授权并删除关联未用登录码 |
| **商品卡草稿与上下架** | `GET /api/admin/apps/:appId/product-drafts?kind=&status=` | 平台管理员 / 该应用运营 | 返回该应用本区个人/企业商品卡草稿（按 `tier_rank ASC`）及当前线上版本摘要 |
| | `POST /api/admin/apps/:appId/product-drafts` | 同上 | 新增会员卡或积分包草稿（初始 `publish_status='new'`） |
| | `PUT /api/admin/apps/:appId/product-drafts/:cardId` | 同上 | 编辑草稿 A 区规则与 B 区文案（若原已上架自动置为 `'modified'`） |
| | `DELETE /api/admin/apps/:appId/product-drafts/:cardId` | 同上 | 删除草稿（若该卡当前已上架，要求先下架或显式传 `forceUnpublish=true` 同事务下架） |
| | `POST /api/admin/apps/:appId/product-drafts/:cardId/publish` | 同上 | 单卡上架/更新上架，生成新不可变 `commerce_versions` 快照 |
| | `POST /api/admin/apps/:appId/product-drafts/:cardId/unpublish` | 同上 | 单卡下架，生成新不可变 `commerce_versions` 快照 |
| **版本回读与货架** | `GET /api/admin/apps/:appId/versions?type=commerce\|pricing` | 同上 | 支撑页头「历史版本 (N)」弹窗列表 |
| | `GET /api/commerce/versions/:versionId` | 运营 / 订单归属用户 | 按版本 ID 回读不可变只读快照 |
| | `GET /api/storefront/products?appId=` | 登录用户 | 按当前付款账户类型 + 本区 + `now()` 解析最新生效已上架商品卡，附带当前订阅对照态（`isCurrent` / `canUpgrade`） |
| **路由与售价** | `GET /api/admin/apps/:appId/route-pricing?kind=` | 平台管理员 / 该应用运营 | 读取当前已发布路由版本 + LLMRouter 最新可用路由版本差异（标记可升级锁版项） |
| | `POST /api/admin/apps/:appId/route-pricing/publish` | 同上 | 校验最小粒度后发布新的不可变 `route_pricing_versions` |
| **同源目录解析** | `GET /api/catalog/models?appId=` | 登录用户 / 阶段 3 网关内网 | 门户与 `/v1/models` 共用解析：`已发布 route_pricing_versions` ∩ `用户权益键/企业应用席位` ∩ `LLMRouter 启用可计量路由` |

---

## 3. 阶段 2b 详细设计：钱包、支付、订单、席位与积分总账

### 3.1 数据库 Schema（`migrations/003_billing_ledger.sql`）

金额统一按最小币种单位整数存储（`amount_minor bigint`：分/美分）；积分统一按百万分之一积分整数存储（`units bigint`：`1 积分 = 1,000,000 units`）：

```sql
-- 1. 现金钱包（个人与企业账户独立；按部署区币种严格隔离）
CREATE TABLE wallet_accounts (
  account_id uuid PRIMARY KEY REFERENCES accounts(id),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  available_minor bigint NOT NULL DEFAULT 0 CHECK (available_minor >= 0),
  arrears_minor bigint NOT NULL DEFAULT 0 CHECK (arrears_minor >= 0),
  frozen_reason text, -- 非空或 arrears_minor > 0 时冻结新购与新预扣
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- 2. 充值支付单（继承原 NeoMei/UniHub 微信 v3 / 支付宝 / Stripe 通道）
CREATE TABLE payment_orders (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  initiator_user_id uuid NOT NULL REFERENCES users(id),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  provider text NOT NULL CHECK (provider IN ('wechat', 'alipay', 'stripe')),
  merchant_id text NOT NULL,
  provider_trade_no text, -- 回调确认后写入，结合 provider + merchant_id 唯一去重
  amount_minor bigint NOT NULL CHECK (amount_minor > 0),
  refundable_minor bigint NOT NULL DEFAULT 0 CHECK (refundable_minor >= 0 AND refundable_minor <= amount_minor),
  status text NOT NULL CHECK (status IN ('pending', 'succeeded', 'failed', 'refunded', 'chargeback')),
  pending_checkout_json jsonb, -- 余额不足跳充值时保存原商品/应用/付款账户上下文
  return_url text,             -- 外部应用聚焦跳入时登记的合法回调地址
  paid_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, merchant_id, provider_trade_no)
);

-- 3. 不可变现金流水总账
CREATE TABLE cash_ledger_entries (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  entry_type text NOT NULL CHECK (entry_type IN (
    'recharge', 'purchase', 'upgrade_return', 'upgrade_return_adjust',
    'original_refund', 'chargeback_debit', 'arrears_repay'
  )),
  delta_minor bigint NOT NULL, -- 正数入账，负数扣款
  balance_after_minor bigint NOT NULL CHECK (balance_after_minor >= 0),
  arrears_after_minor bigint NOT NULL CHECK (arrears_after_minor >= 0),
  ref_type text NOT NULL,      -- 'payment_order' | 'commerce_order' | 'upgrade_record' | 'refund_order'
  ref_id uuid NOT NULL,
  idempotency_key text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- 4. 商品订单与成交快照表
CREATE TABLE commerce_orders (
  id uuid PRIMARY KEY,
  order_no text NOT NULL UNIQUE,
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  buyer_user_id uuid NOT NULL REFERENCES users(id),
  app_id uuid NOT NULL REFERENCES apps(id),
  order_type text NOT NULL CHECK (order_type IN ('subscribe', 'renew', 'upgrade', 'seat_expand', 'credit_pack')),
  commerce_version_id uuid NOT NULL REFERENCES commerce_versions(id),
  card_tier_id text NOT NULL,
  quantity integer NOT NULL DEFAULT 1 CHECK (quantity > 0), -- 个人为 1，企业为席位数
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  paid_minor bigint NOT NULL CHECK (paid_minor >= 0),
  refunded_minor bigint NOT NULL DEFAULT 0 CHECK (refunded_minor >= 0 AND refunded_minor <= paid_minor),
  -- 完整冻结成交时的单卡规则快照 + 升级折算明细/增席补差明细
  snapshot_json jsonb NOT NULL,
  status text NOT NULL CHECK (status IN ('confirmed', 'fulfilled')),
  lago_invoice_id text,
  lago_sync_status text NOT NULL DEFAULT 'skipped' CHECK (lago_sync_status IN ('skipped', 'pending', 'synced', 'failed')),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- 5. 事务发件箱（钱包扣款 + 订单确认 + 待发放记录同事务提交，后台幂等重放）
CREATE TABLE billing_outbox (
  id uuid PRIMARY KEY,
  dedupe_key text NOT NULL UNIQUE, -- 如 grant:{orderId}:cycle:{cycleIdx} / lago_sync:{orderId}
  topic text NOT NULL CHECK (topic IN ('grant_entitlement', 'lago_invoice_sync', 'renewal_notice')),
  payload_json jsonb NOT NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'dead')),
  attempts integer NOT NULL DEFAULT 0,
  next_retry_at timestamptz NOT NULL DEFAULT now(),
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX billing_outbox_poll_idx ON billing_outbox (status, next_retry_at) WHERE status = 'pending';

-- 6. 应用订阅表（个人与企业按 account_id + app_id 独立订阅）
CREATE TABLE app_subscriptions (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES accounts(id),
  app_id uuid NOT NULL REFERENCES apps(id),
  commerce_version_id uuid NOT NULL REFERENCES commerce_versions(id),
  source_order_id uuid NOT NULL REFERENCES commerce_orders(id),
  tier_id text NOT NULL,
  tier_rank integer NOT NULL,
  billing_cycle text NOT NULL CHECK (billing_cycle IN ('monthly', 'yearly')),
  seat_count integer NOT NULL DEFAULT 1 CHECK (seat_count >= 1),
  status text NOT NULL CHECK (status IN ('active', 'expired', 'superseded')),
  auto_renew boolean NOT NULL DEFAULT false,
  -- 自动续费涨价保护：记录授权时的单价，续费报价高于此值须重新授权
  authorized_renew_price_minor bigint,
  period_start timestamptz NOT NULL,
  period_end timestamptz NOT NULL,
  current_grant_cycle_index integer NOT NULL DEFAULT 0, -- 月付为 0，年付按月发放为 0..11
  -- 下周期生效的预约变更：{ targetTierId, targetCycle, targetSeatCount, effectiveAt }
  scheduled_change_json jsonb,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- 同一账户同一应用同时只允许一条 active 订阅（防止并发重复开通/升级）
CREATE UNIQUE INDEX app_subscriptions_one_active
  ON app_subscriptions (account_id, app_id) WHERE status = 'active';

-- 7. 企业按应用独立分配席位表 + 成员周期消费上限 + 一次性邀请链接
CREATE TABLE enterprise_app_seats (
  account_id uuid NOT NULL REFERENCES accounts(id),
  app_id uuid NOT NULL REFERENCES apps(id),
  subscription_id uuid NOT NULL REFERENCES app_subscriptions(id),
  user_id uuid NOT NULL REFERENCES users(id),
  assigned_by uuid NOT NULL REFERENCES users(id),
  assigned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (account_id, app_id, subscription_id, user_id),
  FOREIGN KEY (account_id, user_id) REFERENCES enterprise_members(account_id, user_id) ON DELETE CASCADE
);

CREATE TABLE enterprise_member_limits (
  account_id uuid NOT NULL,
  app_id uuid NOT NULL,
  user_id uuid NOT NULL,
  cycle_limit_units bigint CHECK (cycle_limit_units IS NULL OR cycle_limit_units >= 0), -- NULL 表示不限
  PRIMARY KEY (account_id, app_id, user_id),
  FOREIGN KEY (account_id, user_id) REFERENCES enterprise_members(account_id, user_id) ON DELETE CASCADE
);

-- 按应用订阅发放周期追踪企业成员的在途预扣 + 已结算用量
CREATE TABLE enterprise_member_cycle_usage (
  account_id uuid NOT NULL REFERENCES accounts(id),
  app_id uuid NOT NULL REFERENCES apps(id),
  user_id uuid NOT NULL REFERENCES users(id),
  subscription_id uuid NOT NULL REFERENCES app_subscriptions(id),
  cycle_index integer NOT NULL,
  held_units bigint NOT NULL DEFAULT 0 CHECK (held_units >= 0),
  settled_units bigint NOT NULL DEFAULT 0 CHECK (settled_units >= 0),
  PRIMARY KEY (account_id, app_id, user_id, subscription_id, cycle_index)
);

CREATE TABLE enterprise_invites (
  token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
  account_id uuid NOT NULL REFERENCES accounts(id),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  role text NOT NULL CHECK (role IN ('admin', 'member')),
  created_by uuid NOT NULL REFERENCES users(id),
  expires_at timestamptz NOT NULL,
  claimed_by uuid REFERENCES users(id),
  claimed_at timestamptz
);

-- 8. 百万分之一整数积分批次表（数据库级强守恒约束！）
CREATE TABLE credit_batches (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES accounts(id),
  app_id uuid NOT NULL REFERENCES apps(id),
  source_type text NOT NULL CHECK (source_type IN ('plan_base', 'plan_bonus', 'credit_pack')),
  source_order_id uuid NOT NULL REFERENCES commerce_orders(id),
  subscription_id uuid REFERENCES app_subscriptions(id),
  grant_cycle_index integer,
  granted_units bigint NOT NULL CHECK (granted_units >= 0),
  available_units bigint NOT NULL CHECK (available_units >= 0),
  held_units bigint NOT NULL DEFAULT 0 CHECK (held_units >= 0),
  settled_units bigint NOT NULL DEFAULT 0 CHECK (settled_units >= 0),
  expired_units bigint NOT NULL DEFAULT 0 CHECK (expired_units >= 0),
  -- 升级后旧套餐批次停止新预扣，但允许已有在途预扣结算
  frozen_for_new_holds boolean NOT NULL DEFAULT false,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (granted_units = available_units + held_units + settled_units + expired_units)
);

-- 固化 (expires_at ASC, id ASC) 消费与加锁顺序
CREATE INDEX credit_batches_consume_idx
  ON credit_batches (account_id, app_id, expires_at ASC, id ASC)
  WHERE available_units > 0 AND NOT frozen_for_new_holds;

CREATE TABLE credit_ledger_entries (
  id uuid PRIMARY KEY,
  batch_id uuid NOT NULL REFERENCES credit_batches(id),
  account_id uuid NOT NULL REFERENCES accounts(id),
  app_id uuid NOT NULL REFERENCES apps(id),
  entry_type text NOT NULL CHECK (entry_type IN (
    'grant', 'hold', 'settle', 'release', 'expire', 'upgrade_reclaim'
  )),
  delta_available_units bigint NOT NULL,
  delta_held_units bigint NOT NULL DEFAULT 0,
  delta_settled_units bigint NOT NULL DEFAULT 0,
  delta_expired_units bigint NOT NULL DEFAULT 0,
  ref_id text NOT NULL, -- order_id / hold_id / upgrade_id
  idempotency_key text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- 9. 个人套餐升级返还台账（支持首次返还 + 旧预扣终结后追加补返 + 连续升级封账）
CREATE TABLE subscription_upgrades (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES accounts(id),
  app_id uuid NOT NULL REFERENCES apps(id),
  old_subscription_id uuid NOT NULL REFERENCES app_subscriptions(id),
  old_order_id uuid NOT NULL REFERENCES commerce_orders(id),
  new_subscription_id uuid NOT NULL REFERENCES app_subscriptions(id),
  new_order_id uuid NOT NULL REFERENCES commerce_orders(id),
  old_current_batch_id uuid REFERENCES credit_batches(id),
  p_current_minor bigint NOT NULL CHECK (p_current_minor >= 0),
  f_future_minor bigint NOT NULL CHECK (f_future_minor >= 0),
  b_base_units bigint NOT NULL CHECK (b_base_units >= 0),
  c_cap_minor bigint NOT NULL CHECK (c_cap_minor >= 0),
  returned_so_far_minor bigint NOT NULL DEFAULT 0 CHECK (returned_so_far_minor >= 0),
  finalized_at timestamptz, -- 旧批次 held_units 归零并完成最终补返后置位
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (old_subscription_id)
);
```

### 3.2 支付通道集成边界（继承原 `NeoMei/UniHub` 已验证模块）

- **复用范围**：直接复用原 `NeoMei/UniHub` 的微信支付 v3（Native/H5/JSAPI 拉起 + 平台公钥/证书验签）、支付宝 RSA2 证书模式（PC/WAP 拉起 + 异步/同步验签）与 Stripe Checkout / Webhook 验签模块。
- **新 UniHub 叠加的 4 道防护**：
  1. **强账区与币种校验**：`cn` 服仅允许 `wechat | alipay` + `CNY`，`us` 服仅允许 `stripe` + `USD`，跨区/错币回调直接返回 `400` 且零账本变更；
  2. **三元组幂等去重**：`payment_orders` 强制 `UNIQUE(provider, merchant_id, provider_trade_no)`，并在同一数据库事务内完成「更新支付单 `succeeded` + `refundable_minor = amount_minor` + 钱包 `available_minor += amount_minor`（若有欠款优先抵扣 `arrears_minor` 并按条件解冻）+ 写 `cash_ledger_entries`」；
  3. **结账余额不足回跳闭环**：`payment_orders.pending_checkout_json` 持久化原待购商品、版本、应用与付款账户；入账后门户钱包页和 `/handoff` 聚焦页自动恢复原结账确认卡片，由用户显式二次确认扣款；
  4. **原路退款与拒付冲正**：首期原路退款由运营/客服核实后针对原充值单扣减 `refundable_minor` 与 `available_minor` 走原渠道原路退回；拒付冲正幂等扣减，余额不足以覆盖时差额计入 `arrears_minor` 并立即冻结新购与新预扣（在途已受理任务继续按原预扣正常结算）。

### 3.3 企业按应用独立分配席位与并发控制

- **权限校验升级**（衔接 Stage 1 `accounts.js`）：
  - 购买权限：个人账户默认允许；企业所有者/获授权管理员可在无席位时购买首批席位；企业普通成员购买需同时满足 `can_purchase = true` 且持有目标 `app_id` 的有效席位。
  - 领 API Key 与调用模型：个人账户默认允许；**所有企业成员（含所有者与管理员）均须在 `enterprise_app_seats` 持有目标 `app_id` 有效席位**。
  - 阶段 1 本地验证用 `enterprise_members.has_seat` 保留兼容字段但不自动转换为付费应用席位。
- **防超配事务**：分配或释放席位时，先 `SELECT seat_count FROM app_subscriptions WHERE id = $1 AND status = 'active' FOR UPDATE`，再统计 `COUNT(*) FROM enterprise_app_seats WHERE subscription_id = $1`，仅当 `count < seat_count` 时允许插入，并发分配零超配。
- **成员移除级联**：删除 `enterprise_members` 行通过外键 `ON DELETE CASCADE` 自动清空该成员在所有应用的 `enterprise_app_seats`，即时阻止新购、新发 Key 与新预扣；原有在途预扣按预扣快照归原成员周期正常结算。

---

## 4. 阶段 2c 详细设计：升级折算引擎与 SuperWagie 首套规则

### 4.1 个人套餐立即升档 / 月转年折算精确算法

以旧生效订单 `old_order.paid_minor` 为基准：

1. **按发放月份分摊到最小币种单位（分/美分）**：
   - 设旧订单覆盖 `M` 个发放月（月付 `M = 1`，年付 `M = 12`），`base = floor(paid_minor / M)`，`rem = paid_minor % M`；
   - 第 `k` 期（`0 <= k < M`）分摊额：`alloc(k) = base + (k < rem ? 1 : 0)`（余数依时间顺序分给前几期）。
   - 当前所处发放月为 `k_cur`：`P = alloc(k_cur)`，`F = sum(alloc(k) for k in k_cur+1 .. M-1)`。
2. **折算公式（整数精确运算）**：
   - `B = old_current_batch.granted_units`（仅统计旧当期 `plan_base` 批次，不含 `plan_bonus` 与 `credit_pack`）；
   - `C = max(0, old_order.paid_minor - old_order.refunded_minor)`（该旧单历史可退余量上限）；
   - **升级提交瞬间（同事务）**：
     - 校验钱包 `available_minor >= new_order.paid_minor`（按新套餐全价校验）；
     - 将旧 `plan_base` 与 `plan_bonus` 批次置为 `frozen_for_new_holds = true`（独立 `credit_pack` 保持原余额与到期日不变）；
     - 此时保守已占基础积分 `U_upper = settled_units + held_units`（把在途预扣先视为可能全扣）；
     - 立即返还：`R_now = min(C, F + (B === 0 ? 0 : floor(P * max(0, B - U_upper) / B)))`；
     - 写入 `subscription_upgrades`，将 `R_now` 退回原钱包并累加 `old_order.refunded_minor += R_now`。
   - **旧在途预扣全部终结后（结算或释放钩子触发）**：
     - 当旧批次 `held_units === 0` 且 `finalized_at IS NULL` 时，以最终实际结算量 `U_final = settled_units` 重算：
     - `R_final = min(C_orig, F + (B === 0 ? 0 : floor(P * max(0, B - U_final) / B)))`；
     - `R_delta = max(0, R_final - returned_so_far_minor)`，若 `R_delta > 0` 幂等补返原钱包；
     - 将旧套餐剩余未用 `available_units` 记为 `upgrade_reclaim` 归入 `expired_units` 封账，置 `finalized_at = now()`。

> **样例验算核对（与 `architecture.md` 完全一致）**：
> - `¥99` 月付（`P=9900, F=0, B=2,000M, U=2,000M`）→ `R = 0`。
> - `¥672` 年付第 2 个月（`alloc=5600/月, 已结束1月, P=5600, F=56000, B=2,000M, U=1,000M`）→ `56000 + floor(5600 × 1000 / 2000) = 58800 分 = ¥588`。
> - 连续升级 `A(¥99) → B(¥199) → C(¥399)`：A 基础未用（先扣了临期包）返 `¥99`；B 用 `1/4` 返 `floor(19900 × 0.75) = 14925 分 = ¥149.25`，两笔各自关联原旧单独立封账。

### 4.2 企业增席补差与减席/换档规则

- **企业增席（永久立即生效、对齐原到期日）**：
  - 设当前发放周期总时长 `T_cur`、剩余时长 `T_rem`（按账区时区 `billing_timezone` 秒/日比例精确计算，取整规则：补差金额向下取整到分或向上取整需与补发积分同比例）；
  - 设后续未开始完整发放月数为 `M_future`，单席月价为 `unit_month_minor`，新增 `ΔS` 席：
    - `补差金额 = floor(ΔS × unit_month_minor × T_rem / T_cur) + ΔS × unit_month_minor × M_future`
    - `当期企业共享池立即补发基础积分 = floor(ΔS × base_credits_per_seat × T_rem / T_cur)`（赠送积分同比例补发，有效期对齐当期发放周期结束）；
    - 后续各发放周期按新总席位数 `seat_count + ΔS` 发放。
- **企业减席 / 换档 & 个人降档 / 年转月**：写入 `app_subscriptions.scheduled_change_json`，在 `period_end` 续费边界生效；生效前用户可在 `/console/orders` 一键「取消预约」。
- **自动续费与涨价重授权**：定时任务在 `period_end - 3 days` 写 `renewal_notice` 通知；续费执行时读取当前最新生效商品价，若 `current_price > authorized_renew_price_minor`，中止自动扣款并通知用户重新授权；余额不足则不续费、不发下期积分，原权益保留至 `period_end`。

### 4.3 SuperWagie 首套种子与 DocFlow 复用验证

- **SuperWagie（国内 `cn/CNY` / 美区 `us/USD` 双区各自配置）**：
  - 个人版 4 卡：`Pro 月付 (¥99, 2000+500)`、`Pro 年付 (¥672, 2000+500/月)`、`Max 月付 (¥199, 5000+1000)`、`加量积分包 1,000 (¥39, 90天, 需有效会员)`；
  - 企业版 3 卡：`Enterprise 席位月付 (¥149/席, 3000+500/席)`、`Enterprise 席位年付 (¥1490/席, 3000+500/席/月)`、`企业共享池积分包 10,000 (¥350, 180天)`；
  - 路由定价：固定 5 类云 API 路由（百炼向量 `text-embedding-v4`、百炼重排 `gte-rerank-v2`、火山方舟 `seedream-4.0`、百炼多模态 `qwen3-vl-embedding`、百度智能云文档解析 `paddleocr-vl`）。
- **DocFlow AI**：零业务代码改动，仅通过运营后台配置自己的商品卡（`标准月付 ¥59`、`OCR 加速包 ¥22`、`企业席位 ¥119/席`）与路由白名单即可跑通全部购买、席位、积分池与升级流程。

---

## 5. 生产前端工程落地规范（React + Vite + Tailwind）

1. **构建与托管**：`web/` 使用 React + Vite + Tailwind 本地编译输出至 `web/dist/`，由 UniHub Fastify 服务以静态资源挂载；官网路由（`/`、`/docs`、`/about`、`/brand`）、门户（`/console/*`）、运营后台（`/admin/*`）与聚焦页（`/handoff`、`/invite/:token`）均由同一 SPA 路由承接，API 走 `/api/*` 与 `/login`、`/auth/callback`、`/logout`。
2. **严格 CSP 兼容**：构建产物无任何公网 CDN 请求、无内联 `<script>`、无内联 `onclick`；全局 API 客户端自动从 `GET /api/session` 读取 `csrf` 并在所有 `POST/PUT/DELETE` 请求头附带 `X-CSRF-Token`。
3. **品牌资产**：将 `docs/assets/unihub-logo-v1.png`、`docs/assets/unihub-icon-v1.png` 置入 `web/public/assets/`，全站严格复用 `#087F8C` / `#35C4A3` / `#142B35` / `#F3F8FA` / `#DEE8EC` 令牌与白标隔离规范。

---

## 6. 阶段 2 端到端验收矩阵（PostgreSQL 实测 + `node:test`）

| 阶段 | 编号 | 验收场景 | 预期可观察结果 |
| --- | --- | --- | --- |
| **2a** | `2a-1` | 双应用发布个人/企业不可变版本 + 修改草稿不污染线上 | 修改已上架卡后草稿变 `modified`，`GET /api/storefront/products` 仍返回原版本；再点击单卡「更新上架」生成新版本，其他未上架草稿保持隔离不上线。 |
| | `2a-2` | 按生效时间解析 & 历史版本只读回读 & 数据库写保护 | 同一范围按 `at_time` 精确命中唯一版本；按 `versionId` 可回读旧快照；直接 SQL `UPDATE/DELETE commerce_versions` 被触发器拦截报错。 |
| | `2a-3` | 最小计费粒度校验 & 同源目录隔离 | 将任一已启用路由系数设为 `0` 发布被 `400 granularity_underflow` 阻断；`GET /api/catalog/models` 严格按 `app × kind × region` 返回并过滤无席位/权益键。 |
| **2b** | `2b-1` | 余额不足跳充值再回跳 + 重复/乱序支付回调 | 结账余额不足保留 `pending_checkout_json`；同一 `(provider, merchant_id, provider_trade_no)` 重放 3 次仅入账 1 次；异区币种回调直接拒绝且零账本变化。 |
| | `2b-2` | 扣款后进程崩溃的 outbox 幂等恢复 | 模拟钱包扣款并写入 `billing_outbox` 后进程中断；重启 worker 重放发放权益与积分批次，重复执行不重复发分，现金与积分 100% 守恒。 |
| | `2b-3` | 并发分配应用席位防超配 + 跨应用撤权隔离 | 2 席订阅并发 5 个分配请求仅成功 2 个；撤销应用 A 席位后，应用 B 席位与权限独立不受影响；移除企业成员级联释放其全部应用席位。 |
| | `2b-4` | 一次性企业邀请认领 + 跨区/过期拦截 | 有效链接认领成功并自动加入企业；重复认领、过期或跨区令牌均拒绝。 |
| **2c** | `2c-1` | SuperWagie 升级折算 3 组数值用例 + 在途补返 | 精确复现 `¥0`、`¥588`、`A→B ¥99 / B→C ¥149.25`；旧批次存在在途 `held_units` 时先返已确认部分，在途释放后自动触发追加补返且总返还不超过 `C`。 |
| | `2c-2` | 下周期降档/减席预约 + 自动续费涨价保护 | 预约降档/减席写入 `scheduled_change_json` 且支持取消；续费期商品涨价时自动中止扣款并要求用户重新授权。 |
| | `2c-3` | DocFlow AI 纯配置复用 | 不改一行计费代码，仅发布 DocFlow 商品卡即完整跑通订阅、企业增席、独立积分包先到期先扣及升级折算。 |
