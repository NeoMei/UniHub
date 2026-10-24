# 接入源码核查记录

只读核查日期：2026-10-24。此文件记录当前本地源码，不代表线上版本或真实用户流程已验收。

## SuperWagie

根目录：`/Users/neomei/项目/codexprojects/SuperWagie-open`。当前工作区版本 2.7.6；另一个旧 SuperWagie 路径不存在。无 `.codegraph`，按仓库要求使用限定文件搜索。

- `packages/web/server/lib/ui-auth/ui-auth.js`：实例密码/passkey/设备会话，不是云端个人/企业账户。
- `packages/electron/main.mjs:2039`：parseDeepLink；2155 dispatchDeepLink；6483 附近处理 macOS open-url 和 Windows second-instance。已有 lingxi://，可新增独立平台回调。
- `packages/ui/src/lib/runtime-auth.ts:116` 与 `runtime-fetch.ts:319`：runtime 凭证不能复用为平台凭证。
- `packages/ui/src/lib/api/types.ts:2005`：RuntimeAPIs 可扩展独立 platformAccount 能力。
- `packages/ui/src/lib/opencode/client.ts:1396`：session.prompt；`session-stats.ts:148` 的 session.stats 是展示数据，不是可信账本。
- `packages/web/server/lib/model-configuration/runtime.js:101,266`：模型配置与绑定解析；平台接入需尊重机器企业策略。

## AgentWiki

根目录：`/Users/neomei/.codex/worktrees/agentwiki-autochat-external/agentwiki`。HEAD `24fbbb0499b27282b59929339d001f9531a2a4bd`，核查时无工作区修改。存在 `.codegraph` 目录，但 MCP/CLI 都报告无可用索引；没有创建索引，依指引退回限定源码搜索。

- `apps/server/prisma/schema.prisma:106`：User；201 Space；451 SpaceMember；无独立组织/钱包/订单/订阅模型。
- `apps/server/src/core/auth/auth.service.ts:35`：本地 JWT；146 login；172 register；未发现 OIDC。
- `apps/server/src/core/collaboration/collaboration.gateway.ts:138`：WebSocket 独立验证 JWT，迁移不能只改 REST。
- `apps/server/src/core/authorization/authorization.service.ts:128`：空间资源权限需继续保留。
- `apps/server/src/assist/assist-session.service.ts:70`：任务提交、权限与幂等边界，适合固定账务账户。
- `apps/server/src/assist/assist.queue.ts:262,288`：聊天成功/失败结果未完整保存 usage/cost，不能事后从聊天回算收费。

## llmrouter

根目录：`/Users/neomei/项目/codexprojects/llmrouter`。有可用 CodeGraph，优先读取其当前源码；未修改项目。

- Go/Gin/GORM，已有多数据库、用户、OAuth/OIDC、额度和订阅能力。
- `oauth/oidc.go:51,112`：OIDC token/userinfo，后续可对接统一身份。
- `service/billing.go:20,51`：PreConsumeBilling、SettleBilling。
- `service/funding_source.go:15`：FundingSource 接口；WalletFunding、SubscriptionFunding 为现有路径。
- `service/billing_session.go`：请求级预占、差额结算、退款、追加预占。
- `service/funding_source.go:71` 明确原钱包退款是非幂等增加操作，不能直接拿来重试外部平台退款。

新平台路径应实现独立额度适配器，复用模型路由与计量，避免同时扣本地 quota 和统一平台模型额度。原路径保留到独立产品迁移阶段。涉及 llmrouter 计费表达式改动前须按其 AGENTS 读取 `pkg/billingexpr/expr.md`；保留数据库兼容性和已有溢出防护。

## 已核查开源边界

- Casdoor Apache-2.0；自动续费于 v4.23.0 新增，但本设计由自有服务负责账务，不依赖该新功能。
- Formance Ledger v2.4.16 MIT；作为待实测账本适配器，不能把可见源码等同部署验收。
- Lago 自动充值有 Premium 限制；Kill Bill Aviate Wallet 不属于其开源核心。
- Flexprice v2.1.43 钱包实现位于 internal/ee；该目录有商业生产使用条件。
- UniBee API 为 AGPL，部署包有 license-api 与许可配置，完整免费功能边界尚未实测。

来源：

- https://github.com/casdoor/casdoor/releases/tag/v4.23.0
- https://github.com/formancehq/ledger/blob/v2.4.16/README.md
- https://getlago.com/docs/guide/wallet-and-prepaid-credits/wallet-top-up-and-void
- https://www.killbill.io/aviate/wallet-credits
- https://github.com/flexprice/flexprice/blob/v2.1.43/internal/ee/LICENSE
