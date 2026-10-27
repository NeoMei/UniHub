# UniHub / LLMRouter 本地实现验收

日期：2026-10-28。分支：两仓库 `codex/unified-product-parity`。本记录说明源码、本地构建和隔离验收，不是发布或生产迁移凭证。

## 实现边界

成熟产品保留原用户、认证器、资金账号映射、Key、资源和业务流程。新产品可以引用 UserKit 源码实现自己的用户扩展。两种模式的接口、迁移和资金范围见 [account-integration-modes.md](account-integration-modes.md)；配置及完整认证契约见 [runtime-adapter.md](runtime-adapter.md)。

LLMRouter 普通用户和中央身份用户共用原产品页面、用户 ID、分组、Key、路由、计量和计费计算。中央负责当前授权及显式切换账户的最终账本。管理员额度调整仍是该实例的可消费额度；预算和 Key 上限继续作为消费约束。

## 验收结果

| 层次 | 命令或证据 | 结果 |
| --- | --- | --- |
| UniHub 后端 | 实际 PostgreSQL 与 Formance 环境下 `go test -race -count=1 -timeout=180s ./...` | 通过；含身份、账本、管理授权、恢复与浏览器协议测试 |
| UserKit 源码包 | `packages/userkit-go` 中 `go test -race -count=1 ./...` | 通过；示例编译通过 |
| LLMRouter 后端 | `go test -race -count=1 -timeout=180s ./...` | 全部 68 个有测试的包通过 |
| 静态检查和构建 | 两仓库 `go vet ./...`、主程序构建 | 通过 |
| UniHub 前端 | 全部 42 项测试、TypeScript 检查和 Vite 构建 | 通过 |
| LLMRouter 前端 | 全部 181 项测试、`bun run typecheck`、`bun run lint`、`bun run build` | 通过；lint 仍输出既有警告 |
| 原产品浏览器页面 | `bunx playwright test --config playwright.parity.config.ts` | 13 项通过；使用真实应用界面和受控 API |
| 跨仓库实际 HTTP/TLS | 原注册登录、中央会话、另一实例首次进入、原 Key 和计费接口、实际最终账本 | 通过；可复现命令见 [cross-repository-acceptance.md](cross-repository-acceptance.md) |
| 独立审查 | 身份/授权与资金/迁移分别复核；修复后重跑对应行为回归 | 已处理确认的问题 |

跨仓库链先按原策略给两个实例分别发放 500 单位赠额。第二实例原调用预留 60、实际结算 37，重复结算后余额仍为 463。随后禁用并软删除其原 Key，新调用继续被拒绝；旧已接受调用费用调整至 47，重复调整只补扣一次 10，余额为 453。中央读回同一身份、两个实例绑定，以及付款账号、Key、模型、操作和价格摘要一致的父子调用。原请求数保持 1。

额外恢复测试覆盖已知无入账的拒绝、丢失回复、重复请求、进程重建、慢/坏收据不饿死其他恢复、原 Task/MJ 旧状态覆盖、订阅重复购买、默认 Key/注册赠额只初始化一次，以及钱包和订阅共用 Key 的额度约束。已接受费用在撤权后仍固定原付款责任；未知结果不切换本地钱包，不重发供应商请求，不虚构资金。

## 尚未执行的环境验收

- 未推送、发布、部署、运行生产身份导入或余额迁移，未更改凭据。
- 跨仓库测试验证实际 HTTP/TLS 与 Cookie/重定向协议，不等同 Chromium 在生产域名下的跨站 Cookie 和完整页面验收。
- 真实外部模型、MJ、Realtime 供应商、真实收款、支付渠道对账和供应商结算未在本次本地验收中执行。
- 资金适配当前明确支持 LLMRouter 的 `LR_QUOTA/0`。其他产品可复用身份/授权接入，但须显式实现资产和结算适配。
- 旧批量记账和未知调用责任需要实际停机、核账和备份还原验证。CLI 的 `offline_evidence` 是外部审计证据；它不能自动证明旧节点已退出。已有未知 legacy lease 必须核账，不能直接删除以绕过迁移保护。
- UniHub 前端使用本机已有依赖完成验收；重新下载依赖时，既有 lock 中 `@testing-library/dom@10.4.5` 的远端 404 尚需依赖发布方解决。未改动锁文件。

服务器上线应另做部署、域名浏览器、真实角色和业务账号验收；源码及本地构建通过不能替代这些凭证。
