# 订阅、额度与首个产品接入验收

日期：2026-10-24。状态：UniHub 订阅与用户中心、SuperWagie 首个产品的本地闭环已实现并验收。测试环境、模拟模型结果与生产就绪分别记录。

## 已实现

- 个人和企业团队账户，成员与财务角色校验。
- 产品现金钱包、统一钱包、明确的补款授权与独立续费授权；两者有版本、次数金额界限和失效时间。
- 可配置商品草稿、不可变发布版本；开发环境可显式种入标记为沙盒的测试套餐。
- 月付/年付会员、同周期等级升级、加量包；年付仍逐月发放，按原始 UTC 日期锚点计期，迟到任务只发当前有效月份。
- 模型额度分批管理，预占、补占、按实际用量结算、释放、过期和退款撤回；来源产品可追溯，统一使用 llmrouter 额度池。
- 可信服务 API、按用户/账户/产品/任务/模型绑定的短期模型凭证；用户不能自行决定扣费金额或调用结算服务。
- 八页中文用户中心，后端 OIDC Cookie 会话、CSRF、精确 Origin 校验；客户端没有登录令牌。财务确认展示付费账户、价格和权益。
- 运营商品/费率/订单/退款拒绝 API，使用独立 OIDC audience 和人员名单。

现金为 CNY/6，额度为 CREDIT/6，均由真实 Formance 账本持有数量；PostgreSQL 只保存业务状态、命令与可重建投影。无隐式现金转额度或额度不足自动购买。

## 验证证据

| 范围 | 实际执行 | 边界 |
| --- | --- | --- |
| UniHub 后端 | 全包 `go test -race -count=1 -timeout 120s ./...`，显式配置真实 PostgreSQL/Formance；通过 | 普通无环境变量 `go test` 会跳过集成测试，不能替代此结果 |
| 静态检查 | `go vet ./...`、`git diff --check` | 不替代真实用户流程 |
| 控制台 | 23 项前端测试、TypeScript 和 Vite build | 包含财务角色、整数精度、账户切换旧响应隔离、过期会员重买、产品跳转账户提示 |
| 初始化 | 8 项离线测试；当前数据库重复 `dev-setup` 成功 | 复用已有开发卷，未做生产迁移 |
| 浏览器 | 真实 Casdoor Bob 登录、测试充值、购买月度基础、同周期升级、购买加量包、新团队切换、补款设置读回；390px 无横向溢出 | 明确为合成账户和测试金额 |
| SuperWagie | 完整 Web UI、真实产品后台和安装的 OpenCode 2.1.3；独立 Casdoor public PKCE 登录，个人/团队及会员/额度读取，团队收银台跳转，模型回复与原调用结算回执 | loopback 隔离运行；仅上游模型为模拟，原生打包桌面尚未验收 |
| llmrouter | 真实 Gin → Distribute → Relay → OpenAI 路径、隔离 SQLite、真实 UniHub API/PG/Formance，仅上游为本地模拟 | 没有商业供应商请求或真实供应商凭据 |

控制台初次验收：产品钱包充值 10 元测试金额，购买基础会员，再升级和购买加量包后，SuperWagie 产品现金为 `8000005`（8.000005 元）；共享额度 `3000000`。团队切换后不出现个人资金或套餐。

模型联调采用固定 `unihub-synthetic-model` 与 `local-e2e-chat-v1` 测试费率。供应商返回含缓存/推理的原始用量，规范化为 input/output/cache_read/cache_write/reasoning = `8/4/2/0/1`。精确计算 `8×1 + 4×2 + 2×1/2 + 1×3 = 20` 微额度。JSON 与 SSE 均预占 `65728`、结算 `20`，余量释放。同 ID 重复提交返回 409，错误 task 的查询被拒绝；未知供应商成本没有被伪造为零。首批 4 次本地合成调用总消费 `80`、预留归零，SuperWagie 产品现金仍为 `8000005`。

两项网关修复后的成功调用（另外消耗 40 微额度）：

- `live-e2e-35f9b6c5-3ec9-42c9-b68a-0764c3057a0c`（JSON）。
- `live-e2e-e56549c2-3472-40d5-946d-25941364e844`（SSE）。

HTTP 流式结束和财务终态可能有短暂间隔，必须按原调用 ID 查询；不能因立即读到 RESERVED 就新建调用。上游重试属于网关，未知或已有输出的调用不得重复执行。

## 审查与修复

多轮独立复核已修复：已付款待发放重复购买、续费后的旧期退款、钱包与商业命令相互越序、过期 CREATED 预占、恢复队列阻塞、人工审核误拦钱包、会员过期后无法重买、供应商成本缺失无法结算。新增产品身份审查发现的多 audience 权限混淆和混合 Cookie／设备身份已修复，原始独立复现均通过。网关原始用量缺字段及大小写别名上限绕过也已修复并通过独立复现。产品调用入口、文件独占恢复和关闭后延迟写入均通过独立复现；正常退出与重新启动通过。详见[审查摘要](subscriptions-credits-review.md)。

最终完整 SuperWagie 界面已验证独立 PKCE 登录、会员及六位小数额度显示、个人/团队切换、团队无个人会员或余额、从产品进入 UniHub 后仍选择原团队。控制台登录过期后重新完成 OIDC 登录，也保留团队选择；手动切回个人后显示正确的 `2.999840` 额度及个人订单。原模型任务付款方保持个人账户。

真实客户端验收额外发现并修复网关缺少标准 `tools[].function.strict` 字段。可选布尔值原样传递，非法类型、重复键和别名仍在预占前拒绝；独立 10 场景复核通过。两次被前置拒绝的旧调用没有 UniHub 账务记录，产品 UNKNOWN 日志保留且未重放。当前没有可信的前置拒绝持久回执，不能仅凭 HTTP 400/404 自动解锁旧任务，这是明确的恢复边界。

修复后，调用 `479ecaf4-4293-4926-bd24-f0439fc6acc1` 收到模拟输出但模拟上游遗漏标准 stop 终帧。产品保留 UNKNOWN；点击“核实结算状态”后按原回执恢复 SETTLED，消耗 `20`，没有重新执行上游。补齐模拟上游终帧后，最终调用 `86e855e7-77af-48d4-a043-d9219f7f9381` 在完整 UI 正常返回，无错误提示；产品日志、UniHub 调用记录和 Formance 账本一致为 SETTLED。预占 `66304`，结算 `20`，余量释放。此时共享可用额度 `2.999840`，冻结 `0.000000`，SuperWagie 产品现金仍为 `8000005`。全程没有真实付费模型或支付商户调用。最终聊天费用行显示“UniHub 额度结算”，不再把本地模型价格占位零显示成免费；41 项相关 UI/telemetry 测试、类型检查、lint 和静态构建通过，浏览器刷新原回复验证，未重发模型。

截图及详细机器输出保存在忽略目录 `.local/review/`，包括 `superwagie-full-model-success.png`、`superwagie-final-settled-account.png`、`superwagie-team-checkout.png`、`superwagie-receipt-recovery.png`、`superwagie-full-product-final-settlement.json`、`gateway-real-ledger.json` 与控制台流程截图，不含令牌。临时 fixture 和测试私有配置不作为生产配置提交。

## 尚未完成的外部与产品范围

- 正式价格、权益细则、支付商户、真实充值/支付退款与生产部署。
- SuperWagie 原生打包桌面和长期系统安全存储验收；当前登录不使用 refresh token，重启重新登录。
- 前置拒绝未生成持久回执时的自动恢复；旧 UNKNOWN 日志保留，不能清日志或重放原调用。网关 MySQL/PostgreSQL 部署尚未实测，本次网关使用隔离 SQLite。
- AgentWiki、llmrouter 自有用户门户、璇玑、灵犀、虾说的完整账户接入和迁移。
- 独立运营可视化后台、跨月/年转换、部分退款和人工强制批准；当前拒绝不支持的请求，不伪造成功。
- 真实模型供应商验收，以及未明确支持的辅助生成/压缩/子代理/并行流/实验性协议路径。
- 第三方账户、MCP/API 接入仍属于后续路线图。

此阶段结果不能表述为整个平台目标已经达成或已可生产上线。

## 代码交付位置

- UniHub：本仓库 `codex/account-wallet-foundation`，包含商业领域、用户中心和产品权限边界。
- llmrouter：本地隔离工作区 `.local/integrations/llmrouter`，分支 `codex/unihub-firstloop`，本地提交 `8721c25a9`；未改变原 llmrouter 工作区或推送远端。
- SuperWagie：`/Users/neomei/项目/codexprojects/SuperWagie-open`，平台账户、模型 transport 及对应 UI 已原位修改。按该仓库 AGENTS 要求未执行 Git/GitHub 或提交；改动及使用限制记录在产品 `packages/web/server/lib/platform-account/`、`platform-model/` 的文档中。

完整 llmrouter 审查快照位于 `.local/review/llmrouter-adapter/`，25 个文件及 SHA-256 清单对应上述提交。运行数据、测试登录凭据和模型 journal 均不进入版本库。
