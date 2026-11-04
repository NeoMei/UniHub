# UniHub 功能边界与界面一致性实施计划

> **For agentic workers:** Execute the bounded tasks below with regression checks and independent review.

**Goal:** 让用户控制台和平台后台准确覆盖 v0.4.34 的托管模型、产品自行计价、统一钱包和既有资产边界。

**Architecture:** 复用现有账户权限、查询接口、React 页面和服务端脚本。新增安全查询投影，不修改余额、不迁移身份、不启用真实收款。

**Tech Stack:** Go / PostgreSQL / Formance / React / TypeScript / Vitest。

**Spec:** 本聊天已确认的界面评估与用户“请根据需要修改”授权。

## Global Constraints

- 基于 origin/main ae69c6d（v0.4.34）；不覆盖原目录的既有修改。
- 保留中央用户、原产品用户、原产品 Key、旧额度和历史调用的身份及资产边界。
- 普通成员仅看自己的调用，财务角色可看当前账户；产品和实例隔离不放宽。
- 金额使用精确整数字符串，跨资产不相加；UNKNOWN 不重付，不伪装成功。
- 真实收款开关保持原值；故障豁免仅增加安全只读说明，不新增普通网页写入口。
- 保留当前视觉框架和依赖，不增加组件库。

## Tasks

### 1. 消费查询与后台数据覆盖

Files: internal/billing/query*.go; internal/platformadmin/operations*.go; corresponding tests.

- [x] 先以 runtime 调用与原调用混合数据写失败回归，覆盖成员隔离、产品隔离、分页和资产。
- [x] 查询包含 product_runtime_calls；安全返回 source、instance_id、operation、funding_mode、funding_product_id、refunded、price_version。不暴露摘要、凭证和供应商费用。
- [x] 后台调用、趋势、充值记录包含原生产品来源，并以独立来源 ID 避免冲突；故障豁免提供客户费用释放和供应商待核实只读状态。
- [x] 执行对应 Go 集成测试，记录真实执行与跳过情况。

### 2. 管理操作与接入工作台

Files: web/src/admin/** and its tests; scripts/product_client.py and product_billing.py read-only as contract.

- [x] 回归覆盖 MANUAL_REVIEW、RFC3339 日期区间、UNKNOWN 原命令核对、metered_v2 费率表单及接入计划生成。
- [x] 修正退款状态；日期转为本地日历日边界的 RFC3339；核对只用原 ID/source/version。
- [x] 支持按操作编辑托管模型费率；标明原生产品 Key/价格仍归原产品管理。
- [x] 使用真实脚本契约生成可下载计划和可执行准备命令，明确客户端与私有服务权限；不在浏览器保存密钥。
- [x] 执行相关 Web 测试与构建。

### 3. 用户资金语义与模型消费入口

Files: web/src/App.tsx; forms.tsx; types.ts; api.ts; BillingHub.tsx; new consumption component/tests if needed; styles.css.

- [x] 回归覆盖按账户隔离的消费页面、资产展示、错误/空/未知状态及无财务权限信息隐藏。
- [x] 删除单一钱包扣费断言，说明旧资产保留、团队成员资格不自动授权个人 Key 花费团队资金。
- [x] 账单增加模型消费入口，使用现有 /calls 接口；标明来源、实例、资产、预占/实扣/返还与原付款账户。
- [x] 凭据来源链接保留；改密后结束当前会话界面；密码规则和限流信息可读；能力未确认时购买不提前开放。
- [x] 更新产品指南和密钥/定价归属，执行相关 Web 测试。

### 4. 预算口径与整体验证

Files: internal/billing/usage_budget.go; runtime.go; budget tests; docs/headless-product-api.md; docs/userkit/runtime-adapter.md; api/openapi.yaml as needed.

- [x] 回归证明同账户/产品/实例/CNY 预算按两条通道合计预占和实扣；Key 私有预算不包含他人的原生 Key。
- [x] 复用 UsageTotalsOn 统一读取与执行口径，旧 LR_QUOTA 保持原资产；不修改历史余额或接入模式。
- [x] 更新范围说明与接口契约；运行 Go 测试、真实隔离数据库检查、Web 全套测试与 production build。
- [x] 用本地只读样例预览桌面/平板/手机，独立审查完整 diff 并修复重要问题。

## Execution Notes

三个实现任务使用互不重叠的文件；预算任务由主代理负责。最终统一检查接口、完整差异与验收边界。


## Completion Evidence — 2026-11-04

- Web: 14 files / 93 tests passed; TypeScript and Vite production build passed.
- SDK: 17 protocol tests and strict typecheck passed; OpenAPI parsed with 697 local references resolved.
- Backend: full go vet and full Go race suite passed; after the two review repairs, billing/platformadmin/httpapi race suites passed again with real disposable PostgreSQL and Formance v2.4.16.
- Browser: production build with read-only sample data checked at 1440/768/390; no document overflow or script errors, all three assets and UNKNOWN visible, checkboxes 18×18, and a valid onboarding preparation JSON downloaded successfully.
- Independent review: initial two reporting P2 findings fixed and regression-tested; scoped closure review and independent budget review found no remaining Important/Critical issues. Shared recharge column label uses neutral receipt/order wording.
- Five optional gateway/cross-repository acceptance checks require separately configured repositories and were skipped. No production deployment, supplier call, live OIDC or real collection acceptance is claimed.
