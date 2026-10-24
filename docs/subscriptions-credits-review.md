# 订阅、额度与首个产品集成审查摘要

审查日期：2026-10-24。对象为 UniHub 当前功能工作区、隔离工作区中的 llmrouter 适配器，以及 SuperWagie 当前工作区的产品身份和模型 transport 改动。

已报告的代码问题均完成修复及对应范围复核。商业领域、控制台、产品身份、网关和 transport 的已审查范围通过。首次完整 SuperWagie 请求的 `function.strict` 兼容性缺口已经修复，随后完整 Web UI、模型输出、原调用回执与真实账本闭环通过。最终调用为 `86e855e7-77af-48d4-a043-d9219f7f9381`，结算 20 微额度，产品现金不变；详见[阶段验收](subscriptions-credits-validation.md)。上游是本地模拟模型，这不等于生产部署或真实商业验收。最初被前置拒绝的两个调用没有账务记录，产品 UNKNOWN 日志保留且未重放。

## 范围与证据口径

UniHub 初次审查基于 `9ff93f8`，覆盖商业领域、HTTP/BFF、配置、受限模型凭证、钱包与商业命令排序、前端付款账户及金额边界。后续分别审查产品 OIDC 身份、llmrouter 实际 Relay 路径、SuperWagie 插件/本地代理/持久日志及其公共请求入口。llmrouter 隔离工作区基于 `50fd8d427cf9519f8bfec266bcbd568435c30fbf`。

独立审查先复现具体错误，再对修复做范围受限的复核。账务复现使用隔离 PostgreSQL schema 和真实 Formance 逻辑账本；网关复现使用实际 router/controller/OpenAI 转换链、真实 SQLite 及受控 HTTP 上游；身份复现使用真实签名 JWT/JWKS；transport 复现使用真实 HTTP、中间件与文件 IO。受控上游响应不等于真实付费模型执行。实现者的全套测试、协调者的浏览器验收与独立复现分别记录，不互相替代。

## 原问题、修复与最终复核

以下各项最终处置均为 **ADDRESSED**。

| 范围与原问题 | 修复结果 | 复核证据 |
| --- | --- | --- |
| 已付款但尚未履约的订单没有阻止第二次购买 | 账户屏障包含 PAID/PENDING 及冻结退款；原订单恢复仅排除自身，保留其他未决工作约束 | 原真实 PG/Formance 复现曾产生第二笔成功购买；`TestPaidUnfulfilledOrderBlocksSecondPurchase` 修复后通过 |
| 退还旧账期错误取消独立付费的续期 | 退款核对当前 paid-through 与来源订单结束时间；已变化的订阅转人工核实 | 原复现曾把已续费订阅标为 refunded；`TestRefundPriorTermCannotRevokePaidRenewedTerm` 通过 |
| 钱包操作越过未决商业命令 | 钱包与商业命令共享账户锁及排序屏障，未决资金分配不能被后续转账抢先消耗 | 原真实账本复现曾在支付 UNKNOWN 时完成后续转账；跨队列屏障修复经回归和后续审查确认 |
| CREATED 预占恢复时沿用已过期批次 | CREATED 转 SUBMITTING 前重新检查批次有效期、开始时间及冻结状态；已提交命令仍核对原始不可变载荷 | `TestCreatedReservationRechecksAllocatedBatchExpiry` 在真实 PG/Formance 通过 |
| 已过期但状态仍 active 的会员阻止重新购买 | UI 当前订阅判断同时检查 paid-through，展示与点击路由一致 | 独立真实 Catalog 渲染复现由按钮禁用转为可购买；后端真实账本复现确认同方案可重新购买 |
| MANUAL_REVIEW 退款订单永久阻塞无关钱包操作 | PAID/PENDING 屏障与商业领域一致，只纳入空退款状态或 REJECTED；保留未决命令和 FROZEN 屏障 | 独立 `TestFinalReviewManualRefundDoesNotBlockWallet` 在真实 PG/Formance 复核通过 |
| 标准 usage 没有独立供应商成本时不能结算 | `provider_cost` 缺失/空串表示未知；非空仍须合法整数，幂等摘要区分未知与零 | `TestSettlementOptionalProviderCost`、`TestSettlementRejectsInvalidProvidedCost` 及关联 cap/missing-usage 回归在真实 PG/Formance 加 race 通过；持久读回保持未知，重放只结算一次 |
| 产品多 audience 令牌可被控制台验证器接受 | 共用 OIDC 验证器要求唯一且精确匹配的 audience；非空 azp 必须一致 | 独立真实签名 `aud=[unihub,superwagie]`、`azp=superwagie` 复现现被两验证器拒绝；实现回归覆盖多 aud、重复 aud 和错误 azp |
| 产品 runtime 身份由 Cookie 覆盖已认证设备 bearer | 两个产品路由均使用专用 `resolvePlatformAuthContext`；显式 Authorization 独占身份选择，非法值不能回退 Cookie；无密码浏览器使用独立签名 Cookie | 独立相同攻击 Cookie、两个有效设备的复现现分别得到 `client:a` 与 `client:b`；实现回归覆盖非法凭据回退、签名篡改与重建验证 |
| 实际 OpenCode function.strict 被严格网关拒绝 | `FunctionRequest.Strict` 使用 `*bool` 保留缺失与显式 false；canonical strict 存在时前置要求布尔值，保留别名/重复键拒绝策略 | 独立真实 router 10 场景通过：true/false 出站原值保留；null、字符串、数值、数组、对象、别名、重复键均在 reserve 前拒绝；带合法 strict 的 MAX_TOKENS 攻击仍拒绝 |
| 网关字段大小写别名突破输出预留上限 | 预留前拒绝协议字段别名、未知字段及递归重复键；使用保留数字字面量的严格校验 | 原 `max_tokens:50` 加 `MAX_TOKENS:1000` 复现现 HTTP 400，reserve、上游、settle 和持久 call 均为零 |
| 缺失的 usage 核心计数被 DTO 默认成零后结算 | JSON/SSE 直接观察原始 envelope；核心计数须存在、非 null、为合法整数且总数一致；无效最终观察清除此前暂存 usage | 原缺失 prompt_tokens 的实际 router 复现现保持 UNKNOWN，settle 为零；实现回归覆盖缺失/null/重复/别名及最终 SSE 覆盖 |
| 生产中间件未解析平台 prompt JSON | 在确认模型和可信 Origin 后，由平台路由做有界 JSON 解析；原代理负责重放含服务端 message ID 的请求体 | 独立真实 common middleware 加平台路由复现由 400/admissions=0 变为 200/admissions=1；实现测试还跨真实代理检查 framing 和 BYOK 原始字节 |
| 两个启动者并发删除旧锁后都成为日志 writer | 移除 stale PID 自动 unlink；任何已存在锁均拒绝启动 | 独立真实文件操作调度复现原能打开两个 writer，修复后两个竞争启动均 `platform_store_busy`，writer 数为零 |
| 退出时公共 admission 在锁释放后仍可写日志 | store close 同步封闭 mutation 入口，等待已排队写入再释放锁；后续写入拒绝，重复 close 共用 promise；runtime 拒绝关闭后返回的凭据结果 | 独立延迟 credential、关闭旧 runtime、打开新 writer、再放行旧 admission 的复现现拒绝旧 admission，旧 runtime 不写入，新 writer 状态未被覆盖 |

额外修复检查包括 PREPARED 对账与 DISPATCHED 提交竞争：放弃 PREPARED 的判断移入串行日志写入内部重新检查，不能把已提交调用覆盖为 REJECTED。真实文件事件回归覆盖该交错。正常退出通过既有 awaited graceful cleanup 关闭 transport，并等待私有 HTTP handler 的 UNKNOWN 写入完成后关闭日志。实现者还报告正常 SIGTERM 退出码 0、锁自动移除及随后启动成功。

费用语义保持明确：用户额度消耗由规范化用量与已发布费率计算并受预留上限约束；未知 `provider_cost` 不伪造为零，也不影响已有可信 usage 的结算。`platform_difference` 表示计算额度超过已预留上限的差值，与供应商成本无关。缺失 usage 仍保留待核实状态。

## 实施裁定与保留约束

| 裁定 | 原因与执行边界 |
| --- | --- |
| UniHub 保留现有功能 checkout | 原账户/钱包工作及本地服务已使用 `codex/account-wallet-foundation` 专用目录；本轮以明确文件归属协调，未额外迁移主工作区 |
| 独立任务允许并行 | 商业领域、前端、身份、网关和 transport 按目录/契约拆分；共享 API 与 index 由协调者集成，独立审查单独复核，不把不同任务的测试结果合并成整体验收 |
| llmrouter 使用手工隔离 worktree | 应用 worktree 工具绑定当前 UniHub 仓库，不能选择另一仓库；因此通过 Git worktree 在 `.local/integrations/llmrouter` 建立 `codex/unihub-firstloop`，保留原仓库分支及无关文件 |
| SuperWagie 在原工作区编辑 | 其 AGENTS 禁止未获明确授权的 Git/GitHub 操作；本轮遵守禁令，不执行 Git/GitHub、提交、依赖安装或 changelog 修改，保留无关改动 |
| 产品会话不保存 refresh token | SuperWagie access token 仅在后端内存，返回的 refresh token 丢弃；会话有界且重启需重新登录。该裁定针对产品 BFF；UniHub 控制台可保存不含原始令牌的服务端会话，不应混为一谈 |
| 产品身份不能进入财务权限 | 独立 public PKCE client 使用独立 audience。产品 API 仅开放明确的身份、账户、会员、额度读取及本产品 origin 的模型凭证签发；不授予钱包、购买、转账或结算权限。财务操作仍进入独立认证的 UniHub 控制台，运营另需独立 audience 和 allowlist |
| 模型凭证与登录/服务凭据隔离 | 模型凭证绑定 actor、account、origin、task、model 和期限，UniHub 仅保存摘要；不能替代登录或结算服务身份。网关服务凭据留在受信任后端 |
| 原任务付款方不可随设置切换 | 产品提交前记录经验证的 actor/account/runtime/model/task；后续账户切换只影响新任务。产品跳转控制台携带公开账户提示，无法访问该账户时明确拒绝，不静默改用个人账户 |
| 日志崩溃锁 fail closed | 任意已有 `unihub-model/writer.lock` 都阻止新 writer。崩溃恢复须先核实所有使用该数据目录的实例已经停止，保留 `state.json`，只移除锁文件，再启动单个实例。不能仅根据 PID 或等待时长自动删除锁，不能删除日志来清除未决调用 |
| 不确定调用不重发上游 | DISPATCHED/UNKNOWN/未结算状态保留原 logical call 和 payer；同 ID 内容变化拒绝，新的调用 ID 不能绕过未决阻塞。对账读取同绑定 receipt，由网关恢复原终态命令 |

## 已完成验证与适用范围

- 商业实现报告记录 26 个包内测试通过，含 24 个真实 PG/Formance 集成用例和 race；后续 optional provider cost 的独立真实账本/race 回归通过。协调任务报告过全 Go 12 包 race 和 8 个 bootstrap 检查通过。这些是各自记录时点的结果，不作为之后所有代码版本已全量重跑的证明。
- 控制台已有真实 Casdoor 登录，以及本地 sandbox 充值、购买、升级、加量包、团队账户切换和默认关闭授权的浏览器证据。前端最终相关账户提示改动报告 23 个测试和构建通过，并验证团队链接选择团队账户。
- 产品身份修复有真实签名 JWT 与跨设备复现；实现者记录相关 70 个 auth/account/model/core 测试通过。真实本地 Casdoor 产品登录已成功，后端 token 不暴露给 UI。
- 网关严格解析修复后，实施者记录 8 个相关包通过，platform/router 财务回归加 race 通过；独立原始攻击复现通过。协调者此前完成实际 UniHub HTTP、PostgreSQL、Formance 配合受控供应商 JSON/SSE 的验收，两种调用各结算 20 microcredits，重复调用 409、错误 task 的 receipt 拒绝。此后完整 UI 的成功与回执恢复已由协调者单独验证。
- transport 最终实施者记录 65 个组合测试及 UI 构建通过；独立复现确认请求解析、锁竞争及退出写入修复。安装的 OpenCode 2.1.3 在隔离 HOME、无继承供应商密钥、仅 loopback 网络中完成 6 次受控模型调用，覆盖普通轮次、工具循环、未绑定/内部 prompt 拒绝、部分输出不重试及 receipt 后恢复。

## 实际产品请求兼容性补充复核

`function.strict` 补丁仅扩展明确的 OpenAI 函数工具字段，未放宽任意未知字段。独立临时 overlay `TestGatewayReviewFunctionStrict` 跨实际 router/distributor/controller/OpenAI 转换链执行 10 个场景。true 和 false 均在供应商 HTTP 请求体中保持存在及原值，结算一次，重复调用返回 409。null、字符串、数值、数组、对象、`STRICT` 别名和重复 strict 键均返回 400，reserve、upstream、settle、release 和持久 call 均为零。合法 `strict:false` 与 `MAX_TOKENS` 混用仍在预留前拒绝。8 个场景首轮通过，字符串/数值两个补充场景另轮通过。

该问题最终为 **ADDRESSED**，此次范围的 spec/quality 复核通过，未发现补丁引入的新问题。实现者原有 DTO round-trip 与 router 回归已审读；本次没有修改生产代码，也没有用组件复核替代完整 UI 验收。

## 仍未完成或未验证

1. 网关前置拒绝当前可能没有持久回执；产品不能凭 HTTP 400/404 推断未执行。两个历史失败测试调用保留 UNKNOWN，未扣费、未重放，也没有自动解锁。后续可增加绑定 actor/account/task/digest 的可信拒绝回执。
2. 完整 Web UI 的正常回复与原回执恢复已确认；主流真实模型供应商、原生桌面及所有异常用户流程仍不能由该本地证据推断。
3. 未执行真实外部付费模型请求或真实商户支付/退款；本地测试商品、价格、额度与供应商输出均不代表生产商业配置或供应商可用性。
4. 网关持久化实际运行验证使用 SQLite；MySQL/PostgreSQL 的 gateway GORM 部署尚未执行。UniHub 自身的真实 PostgreSQL 测试不能替代该项。
5. 原生打包桌面、OS 凭据存储、正式部署、监控和跨机器异常恢复未验收。移动端、VS Code、外部 OpenCode、辅助 generation、child session、command/resume、非文本及实验 transport 当前明确不支持，不能推断已覆盖。
6. 会员年/月边界、授权启用、角色变更、转账、退款和未知结果等已有领域/接口测试，但控制台完整真人浏览器矩阵尚未全部执行。部分退款、跨月/年升级、任意退款批准和完整运营界面仍属明确未开放能力；当前人工退款处置只支持拒绝并解除相应冻结。
7. 崩溃后人工锁恢复流程已文档化，自动 stale-lock 接管刻意不提供。现有正常退出/重开测试不等于生产故障恢复演练。

## 详细记录

本摘要保留关键结论，详细时间顺序与复现保存在 `.superpowers/sdd/2026-10-24-subscriptions-credits-console/` 的 `task-1-review.md`、`final-review.md`、`product-identity-review.md`、`gateway-review.md`、`transport-review.md` 及对应 implementation reports。早期记录中的 pending 和旧行为由后续 scoped recheck 更新。临时 `/tmp`、`.local` 复现路径仅是本地审查证据，不作为部署依赖。本文整合审查记录与协调者最终验收证据；文档更新没有修改生产代码或重复运行未变化的测试。
