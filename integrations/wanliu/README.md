# 万流接入 UniHub v2

万流在同一进程内处理两类凭据。原生 Key 走已有的 New API 路由，UniHub Key 先向 UniHub 查询目录和预扣，再携带专用的原生中继 Key 调用模型。国内首个经过分项计量验证的路由为 `deepseek:deepseek-v4-pro`，支持普通和流式 chat completions。

## 应用到源码并构建

准备与目标实例一致的源码和前端资源，在 UniHub 仓库根目录运行以下命令。已集成 Antigravity Stage 3 的万流 rc.28 使用 `router-rc28.patch`。上游 New API v1.0.0-rc.51（c9c6da278）使用 `router-rc51.patch`。

```sh
integration="$(pwd)/integrations/wanliu"
cd "$WANLIU_SOURCE_DIR"
git apply --check "$integration/$WANLIU_ROUTER_PATCH"
cp -R "$integration/service/wanliubilling" service/
git apply "$integration/$WANLIU_ROUTER_PATCH"
go test ./service/wanliubilling
go build -o new-api .
```

rc.28 保留已有的私有管理、原生渠道、前端和数据存储。启动时用该版本支持的 `SERVER_BIND_ADDRESS=127.0.0.1` 指定监听接口。rc.51 使用 `HOST=127.0.0.1`。原生实例、数据库和前端构建步骤沿用各自的发布流程。

## 生产设置

| 配置 | 值 |
| --- | --- |
| `UNIHUB_V2_BILLING_ENABLED` | `true` |
| `UNIHUB_UPSTREAM_SIMULATOR` | `false` |
| `UNIHUB_REGION`、`UNIHUB_CURRENCY` | `cn`、`CNY` |
| `UNIHUB_INTERNAL_URL` | UniHub 私有入口，根地址 |
| `UNIHUB_GATEWAY_SERVICE_TOKEN` | 与 UniHub 配对的计费服务凭据 |
| `UNIHUB_EXECUTOR_ID` | 当前实例标识 |
| `UNIHUB_UPSTREAM_BASE_URL` | 万流原生根地址，可使用本进程回环地址 |
| `UNIHUB_UPSTREAM_API_KEY` | 专用原生中继 Key，授权 DeepSeek Pro、限制来源与额度 |
| UniHub 的 `LLMROUTER_ADMIN_URL`、`LLMROUTER_ADMIN_TOKEN` | 指向万流私有入口，用计费服务凭据同步 Key 变更 |

在目标应用的国内定价版本中追加 Pro 路由，并保留现有路由。定义见 `src/commerce/deepseek-pricing.js`。目录显示高峰和空闲分项价。网关以高峰价预扣输入上界与输出限额，再用供应商实际回报的缓存命中、未命中和输出 tokens 按适用时段结算。默认日历覆盖 2026–2027 年。传输结果或用量无法确认时，保留 hold 供 UniHub 对账。可按返回的 hold ID 和原生请求号检查两侧日志。客户端重试同一逻辑请求须复用 `X-Request-Id`。

## 验收

安装 UniHub npm 依赖，准备 PostgreSQL 的 `initdb`、`postgres` 和本机可执行的候选网关。设置 `PG_BINDIR` 和 `WANLIU_GATEWAY_BIN` 后执行 `node integrations/wanliu/smoke.mjs --mock`。使用 `--real` 时设置原生入口和 QA 中继 Key。详见 `--help`。两种模式均创建一次性数据库和付款账户。真实模式发起一笔最多 8 个输出 tokens 的请求，报告返回的原生请求号、hold、预扣、结算、释放与前后余额。
