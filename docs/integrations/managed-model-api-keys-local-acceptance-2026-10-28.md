# 长期模型 API Key 与产品预算：本地集成验收

日期：2026-10-28。范围：UniHub 与 LLMRouter 本地源码验收。此报告不证明发布或服务器更新；部署单独记录。

## 实现边界

- UniHub 保存长期 API Key 摘要，绑定当前用户、付款账户、产品、实例、模型、资金模式和资产；创建响应只返回一次完整密钥。十分钟任务凭证保留原任务绑定和期限。
- 新接口为账户下的 `api-keys` 创建、列表和撤销，以及 `usage-budget` 读取和修改。产品/实例由已验证 client registry 决定；普通成员只管理本人密钥，账户 owner/billing_admin 管理本实例的账户密钥和共享预算。
- 预算是相应资产的累计消费上限，不是钱包余额，也不授予资金。空上限为不限，零上限阻止新预占；共享预算使用版本比较交换。两类上限均在原中心账户锁和 reserve/topup 流程内生效。
- 未完成、缺失用量或结果未明的调用保留预占；结算按最终实扣计入 `used`，释放恢复可用预算。重放不重复计费。旧调用摘要、账本余额、历史密钥和旧身份不自动迁移或合并。
- 用户/产品停用、产品权限撤销、失去付款账户成员资格、密钥到期或撤销都会阻止新增使用。撤销或到期的 bearer 也不能读取公开模型、任务、结果或 receipt；独立可信服务仍能按存储的调用完成结算或对账，历史页面使用当前产品会话授权。

## 真实本地链路

`TestManagedAPIKeyActualStandardGatewayAcceptance` 使用真实 UniHub HTTP handler、隔离 PostgreSQL schema、真实 Formance，以及重新编译并单独启动的 LLMRouter router 测试进程。供应商响应和 OIDC 验签器是受控夹具；供应商适配器、中心调用/账本、LLMRouter 会话签发及监控路由执行实际代码。

1. 通过 UniHub HTTP 创建长期模型密钥，为 llmrouter 钱包和其他钱包建立各自的 sandbox 测试资金。
2. 使用标准 bearer 调用 LLMRouter `/v1/models`、普通和流式 `/v1/chat/completions`，读取实际供应商适配器返回。
3. 在中心完成首次结算后故意丢失响应，再重启 LLMRouter。恢复仅重放结算，不重新发送供应商请求；相同显式幂等键返回 409。
4. 两个逻辑调用各结算 55 个 CNY/6 单位，中心实扣和预算 `used` 均为 110，`held` 为零。llmrouter 钱包余量 999890；其他产品钱包 2000000、统一钱包 3000000 均保持各自余额。
5. 通过实际 `/api/unihub/session` 交换已验证中心产品会话，使用实际签发的 JWT 读取原 `/api/log/self`、`/api/data/self`、`/api/data/flow/self`。日志包含两个中心调用和技术元数据；仪表盘显示两次请求、30 个可信 token，未创建本地额度消费。
6. 原生兼容标记用户的额度 12345、已用 7、请求数 3，以及原生 token 余量 54321、已用 11，在调用和进程重启后保持不变。路由主体的本地额度及已用仍为零。
7. 撤销密钥后，标准调用和公开 receipt 读取被拒绝，不增加供应商请求。

## 验证结果

- `go test ./... -count=1`：使用真实本地 PostgreSQL/Formance，并设置绝对路径 `UNIHUB_LLMROUTER_PATH`，全部包通过。包括已有短期凭证的 credits/prepaid 实际网关验收，以及新增长期密钥完整链路验收。
- `go test -race ./internal/httpapi ./internal/billing -run 'Test(DurableAPIKey|APIKey|ManagedAPIKeyRechecks|ManagedKeyBudget|UsageLimit)' -count=1`：通过。
- SDK `npm test`：16 个测试通过；`npm run typecheck`：通过。
- 新 HTTP 用例覆盖密钥摘要/不回显、模型与实例隔离、到期/撤销拒绝、成员移除、财务管理员降权、成员读取共享预算拒绝、权限角色撤销、共享预算版本冲突、并发预占、追加预占、释放和结算重放。
- 新账务用例覆盖 reserve/topup 结果丢失后的保守预算占用、恢复、降低上限以及释放；大于 JavaScript 安全整数的精确数量和零上限均通过。
- OpenAPI YAML 解析和全部内部引用解析通过；`git diff --check` 通过。

完整链路曾捕获监控查询以整秒作为闭区间上界而漏掉当前秒新调用的问题；LLMRouter 已增加固定时间回归用例并改为包含完整结束秒的区间，随后完整链路和全量测试通过。

## 尚未覆盖的证据层级

本报告不代表实际 Casdoor/OIDC 浏览器登录、付费供应商请求、真实支付、服务器升级或生产用户回读。测试使用隔离数据和受控供应商；没有自动迁移旧余额、旧密钥或旧身份。
