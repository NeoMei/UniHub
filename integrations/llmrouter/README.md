# llmrouter 通用资产计费桥接

`composable-billing.patch` 是对已有 UniHub 平台计费桥接的一次通用升级，基线为 llmrouter `8721c25a91c1d5f74eb3a293d6ed1687661b3a38`。业务修改位于 `internal/platformbilling`，另包含显式启用的真实数据库迁移测试及 `router/platform_live_fixture_test.go` 接线。各来源产品继续使用相同的平台调用流程，无需实现自己的扣费逻辑。

## 接口与兼容

- 仅使用受信 UniHub 验证返回的 `asset`、`funding_mode`、`billing_profile_version` 选择资金路径；资产只接受 `CREDIT/6` 和 `CNY/6`，对应模式为 `credits` 和 `prepaid_cash`。历史空值使用 `CREDIT/6`、`credits`、版本 0。
- 同一模型可以同时发布 CREDIT 和 CNY 费率。`Rate.asset` 空值按 CREDIT 处理；费率必须与凭证资产一致，不能自动切换资金或混算资产。
- 本地调用保存资产、资金模式、`llmrouter` 资金产品和配置版本。GORM 新列带历史默认值；历史表迁移已在 SQLite、真实 MySQL 5.7 和 PostgreSQL 9.6 上验证。后两者使用新建、仅绑定回环地址的临时容器，连续运行迁移两次，确认预先存在的 SETTLE_PENDING、UNKNOWN、RESERVED 调用保持付款方、摘要、费率、结算意图和状态，新增资金字段采用 CREDIT 历史默认值。目标部署仍需按其现存数据和备份流程验收。
- 版本 0 的历史 CREDIT 请求摘要保持原字节算法。新资金或配置版本加入带版本前缀的摘要，重复调用同时核对不可变资金快照；不会再次请求预占或发送供应商请求。
- 预占请求体保持原协议。预占、结算及恢复响应额外核对 `charge_asset`、`funding_mode`、`funding_product_id`、`billing_profile_version` 和 `rate.asset`。现金调用明确要求资金产品为 `llmrouter`；历史 CREDIT 响应可省略新增字段。
- UNKNOWN 调用继续等待核实；丢失预占响应的调用只按原快照查询并释放。结算响应不匹配时保留原持久化结算意图，重试原幂等命令。网关不从本地钱包再次扣款。
- 收据新增 `asset`，返回原调用资产。新凭证仍须通过 actor/account/origin product/task/model/expiry 授权；当前配置改变资金模式不妨碍读取有权访问的旧收据。

## 费率固定配置

原 `UNIHUB_MODEL_RATE_IDS`（`{"model-name":"credit-rate-id"}`）只固定 CREDIT 费率，保留现有行为，不影响 CNY 的同模型费率选择。

可选新增公共配置 `UNIHUB_MODEL_ASSET_RATE_IDS`，按资产、模型固定费率，并优先于该资产的旧配置：

```json
{
  "CREDIT/6": { "model-name": "credit-rate-id" },
  "CNY/6": { "model-name": "cash-rate-id" }
}
```

该配置只包含公开费率 ID，不存放服务令牌或用户凭证。没有固定配置时，每个模型与资产必须唯一匹配一条费率。

## 本地验证与边界

已执行并通过：

```sh
go test ./internal/platformbilling ./service ./router -count=1
go test -race ./internal/platformbilling -count=1
```

定向测试涵盖双资产费率、旧固定配置、新资产固定配置、无效资金凭证、历史摘要/空字段/表迁移、调用 ID 冲突、预占响应丢失、错误资产/费率资产/资金产品/模式/配置版本的响应及恢复、结算重试、原资产收据。真实接线与数据库测试受环境开关控制，普通套件通过不代表已执行它们。

### 已执行的真实接线验收

UniHub 新增 `internal/httpapi/gateway_acceptance_test.go`。它构建并启动实际 llmrouter router/controller/OpenAI adapter，经回环 TCP 调用实际 UniHub HTTP 服务，使用隔离 PostgreSQL schema 和真实 Formance ledger。仅模型供应商响应及登录身份验证使用测试夹具；不读取供应商密钥库、不使用现有账户、不占用固定端口。

| 场景 | 资金与结果 |
| --- | --- |
| 未声明配置的版本 0 hybrid，正常付费会员 | 选 CREDIT 费率；8/4/2/0/1 互斥用量扣 20 CREDIT/6；可用额度 999980，已用 20，预占清零；llmrouter 现金保持 1000000 |
| prepaid 配置，无任何订阅，流式模型请求 | 同一模型选 CNY 费率；相同用量扣 55 CNY/6；llmrouter 现金 1000000 → 999945，收入 55，预占清零 |
| 两种场景分别丢失一次真实结算响应 | 第一个网关进程保留 SETTLE_PENDING；保留 SQLite 调用库并重启，恢复为 SETTLED 收据，保持原资产与金额；重放请求返回 409，不重新发送模型请求 |

每种场景仅派发一次供应商请求；重启后派发计数为零；网关本地 quota/used_quota 都为零。来源产品现金与统一钱包均保持原余额，真实 UniHub 只存一条逻辑调用，结算恢复未二次扣款。上述为本地 HTTP/TCP 接线和数据库/账本验收，不是 Casdoor 登录、真实模型供应商、线上部署或真实支付验收。

在上述 llmrouter 基线的独立检出应用补丁后，从 UniHub 根目录运行：

```sh
# UNIHUB_TEST_DATABASE_URL、UNIHUB_TEST_LEDGER_URL 指向本地测试 PG/Formance。
# 测试自动创建并清理独立 PG schema，Formance 使用独立随机账本名。
export UNIHUB_LLMROUTER_PATH=/absolute/path/to/patched-llmrouter
go test -race ./internal/httpapi -run '^TestComposableBillingActualGatewayAcceptance$' -v -count=1 -timeout=10m
```

未设置 `UNIHUB_LLMROUTER_PATH` 时该用例直接跳过。网关 fixture 的 origin/listen/database/ready 参数均由用例提供，默认不改变原有手动本地夹具行为。

真实 GORM 迁移验收从 llmrouter 根目录运行；两个 DSN 必须各指向全新可丢弃数据库，因为用例会重建 `platform_model_calls`、`platform_model_attempts`：

```sh
# 由本地测试容器提供 UNIHUB_GATEWAY_MIGRATION_MYSQL_DSN 和
# UNIHUB_GATEWAY_MIGRATION_POSTGRES_DSN，凭据不写入文档或命令行。
UNIHUB_GATEWAY_MIGRATION_DISPOSABLE=1 go test ./internal/platformbilling -run '^TestFundingMigrationRealDatabases$' -v -count=1
```

状态：源码修改目前位于本机既有隔离集成检出；此目录将修改保存为可审阅补丁。未提交、未推送 llmrouter，未部署。目标 llmrouter 版本需核对补丁上下文后应用，重新运行上述套件及真实接线验收，再进入其部署验收。
