# 账户与资金 API

当前接口服务第一阶段的基础切片，使用 Casdoor **access token**：`Authorization: Bearer <access_token>`。从本地 PKCE helper 获取令牌的方式见[开发说明](local-development.md)。没有匿名用户接口、可信用户 header 或浏览器 cookie 会话。

## 账户与权限

| 方法与路径 | 用途 |
| --- | --- |
| GET /v1/me | 验证身份并取得 UniHub 用户 ID；首次调用建立个人账户 |
| GET /v1/accounts | 列出本人可访问账户及角色 |
| POST /v1/accounts | 创建企业团队，body 为 `{"name":"团队名称"}` |
| GET /v1/accounts/{account}/members | 读取团队成员 |
| POST /v1/accounts/{account}/invitations | body `{"user_id":"受邀用户ID","role":"member"}`；不会发送邮件/消息 |
| POST /v1/invitations/{invitation}/accept | 仅受邀用户可接受 |
| PATCH /v1/accounts/{account}/members/{member} | body `{"role":"member"}` |
| DELETE /v1/accounts/{account}/members/{member} | 撤销团队成员资格；不能移除最后一个 owner |
| GET /v1/products | 读取启用的自有产品目录 |

邀请按用户 ID 定位，不以相同邮箱自动合并身份。管理员只能管理普通成员；owner 管理财务角色。新增 owner 必须经过邀请及本人接受。已经失权的邀请人不能让其旧邀请继续生效。

只有 owner、billing_admin 能管理与查询资金。操作的 `account` 始终固定，切换账户不会迁移已有订单或余额。

## 充值与划拨

现金资产为 CNY/6：`"1000000"` 表示人民币 1 元；接口金额必须为十进制整数字符串。外部充值还要求整分，例如 1 分为 `"10000"`。不接受 JSON 数字、小数、负数或科学计数法。

| 方法与路径 | 用途 |
| --- | --- |
| GET /v1/accounts/{account}/wallets | 统一钱包与各产品钱包的账本余额，空 product_id 表示统一钱包 |
| POST /v1/accounts/{account}/recharges | body `{"product_id":"","amount":"100000000"}`；创建统一钱包充值 100 元的订单 |
| GET /v1/accounts/{account}/recharges/{order} | 查询充值单及对应记账操作 |
| POST /v1/accounts/{account}/recharges/{order}/sandbox-settle | 仅本地 sandbox：模拟已核验支付通知；进入真实账务命令和恢复路径 |
| POST /v1/accounts/{account}/transfers | body `{"product_id":"superwagie","amount":"80000000"}`；从同一账户统一钱包划拨 80 元 |
| GET /v1/accounts/{account}/operations | 最近 100 条操作 |
| GET /v1/accounts/{account}/operations/{operation} | 单笔记账状态 |
| GET /v1/accounts/{account}/audit | 最近 100 条账务审计事件 |

创建充值与划拨必须携带 `Idempotency-Key`（1–128 字符）。同一个账户、同一种操作复用 key 和相同参数返回原操作；改变参数返回 409。内部充值记账与用户转账的 key 属于不同命名空间。

创建充值单返回 201，资金尚未到账。收到可信支付通知后建立记账操作；划拨与 sandbox 确认返回 202。轮询操作状态，只有 `APPLIED` 表示账本已确认。`REJECTED`（例如余额不足）与 `CANCELED` 为终态，不能把未知状态显示成成功或用新 key 自动重试。

付款通知 `/webhooks/sandbox` 仅开发 sandbox 开启，使用 HMAC-SHA256：待签名内容为时间戳字符串、`.`、原始 HTTP body；头为 `X-UniHub-Timestamp`、`X-UniHub-Signature`。时间差最多五分钟，密钥至少 32 字节。签名通过后仍校验商户、订单、币种、金额、终态及支付交易唯一性。生产模式不能启用 sandbox，也没有已集成的真实支付渠道。

## 恢复边界

操作状态为 `CREATED → SUBMITTING → APPLIED / REJECTED / UNKNOWN`。Worker 对同一账户串行执行，重新查询/提交时始终沿用原账本 reference 和幂等键。未提交的转账在操作者财务权限撤销后取消；已经进入提交的操作继续核实原结果。

`UNKNOWN` 会暂停该账户后续操作，但不阻塞其他账户。账本查询不到交易并不能证明原请求永远不会提交，因此此前未知的操作不会仅因一次重放返回余额不足就被自动判为失败。长期未知或账本分录不匹配仍需要运维核实；当前没有强制改账或人工结案接口。

## 错误与范围

401 表示认证失败；403 无当前账户权限；404 不存在于可访问范围；409 幂等或业务状态冲突；400 输入无效；503 依赖不可用或支付渠道未配置。错误响应不返回数据库 DSN、密钥或上游执行细节。

接口目前提供有限数量列表，无通用分页或报表。自动充值授权、会员/额度、退款与运营调整尚未进入此切片；不能直接在数据库编辑余额，也没有这类余额字段。代码中的现金余额读取都来自 Formance。
