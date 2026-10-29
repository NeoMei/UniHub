# UniHub 平台后台

平台后台使用中央控制台会话和数据库中的平台角色。团队 owner、产品管理员、Casdoor `isAdmin`、账单 operator、模型 Key 和产品服务凭据都不会自动获得平台权限。

## 首次启用

1. 确认目标账号已登录过 UniHub，记录 `/v1/me` 返回的中央 UUID。
2. 使用受保护的部署工具调用平台 authority 的 bootstrap 操作，为精确 UUID 授予 `super_admin`；不能按邮箱、用户名或“首个注册用户”推断。
3. 访问 `/admin/v1/me` 读回角色和细粒度权限，再打开 Web 的 `/admin/users`。
4. 原 `USER_ADMIN_IDS` 仍作为旧用户管理兼容资格导入。它只提供旧目录、状态和产品角色范围，不能授予财务或平台管理员权限；数据库中的撤销记录优先于后续重启导入。

平台角色：

- `super_admin`：全平台管理和受控财务/身份操作。
- `user_admin`：用户、普通团队支持和产品角色；不能读财务金额、管理平台管理员或授予团队高权限。
- `finance_admin`：账务、订单、退款复核、商品/费率/计费配置和安全元数据。
- `auditor`：脱敏只读审计、运营和财务记录。

## 常用入口

| 页面 | 路径 |
| --- | --- |
| 管理概览 | `/admin/overview` |
| 用户与详情 | `/admin/users`、`/admin/users/{id}` |
| 注册身份 | `/admin/identities` |
| 平台管理员 | `/admin/admins` |
| 账户与团队 | `/admin/accounts`、`/admin/accounts/{id}` |
| Key、调用、预算 | `/admin/api-keys`、`/admin/calls`、`/admin/usage-budgets` |
| 账务与订单 | `/admin/wallets`、`/admin/transactions`、`/admin/orders`、`/admin/refunds` |
| 审计与状态 | `/admin/audit-events`、`/admin/system-status` |

所有列表使用有界游标分页、筛选和总数。平台范围与当前付费账户工作区分离，切换个人/团队账户不会改变平台权限。余额以 Formance 为准；账本不可用时显示不可用，不以零代替。现金、模型额度、sandbox 交易、真实到账和退款状态分别展示。

## 安全边界

停用用户会撤销 UniHub 浏览器会话、推进 bearer 失效时间并撤销该用户的长期 API Key；已发起调用的可信结算仍可按原绑定完成。重新启用不会恢复旧凭据。完整上游 SSO/刷新令牌撤销只有在 pinned Casdoor 适配和部署等价验收开关都启用时才显示成功，否则返回待核对或能力不可用。

用户创建、激活和密码恢复只向既有已验证渠道交付一次性凭据；管理员不会看到密码或恢复 secret。没有验证渠道的身份显示为不具备自助恢复条件。未知的上游结果禁止重复创建或重复改密。

钱包、账本、订单和退款不能通过平台页面直接改余额、删除历史或把 `UNKNOWN` 命令强制标为成功。恢复操作沿用原 command/reference/idempotency key，并记录真实平台操作者与原因。所有写操作需要版本和原因；审计事件没有编辑或删除入口。

## 身份适配开关

`ADMIN_IDENTITY_DEPLOYMENT_VERIFIED`、`ADMIN_PASSWORD_RESET_VERIFIED`、`ADMIN_SESSION_REVOCATION_VERIFIED` 默认关闭。仅在锁定 Casdoor v4.23.0 审计构建、真实部署读回和脱敏/会话/重置验收完成后逐项打开。源码 fixture 或本地页面不能证明生产能力；真实邮件发送与生产权限变更需要单独的运维授权。

当前生产部署状态（2026-10-29 核验）：部署、撤销与密码重置三个开关已打开。运行容器为钉住审计构建（upstream commit `1b91d9d319a127e9b1af88fa7a20bb35c052b9d8`，镜像 ID 与 `shared/headless-audit.json` 记录一致，`/server` 校验和匹配）；当日重新执行受控探针（注册 201、跨源 403、错误密码 401、授权 200、令牌 200、重放 400），并对探针用户完成真实撤销：`delete-session` 返回 Affected，会话清零，关联令牌 `expires_in` 由 3600 置 0，原 bearer 访问 userinfo 被拒绝并提示令牌已过期。

v0.4.21 已使用独立临时账号完成真实邮箱密码恢复验收：管理员发起、HTML/纯文本双语邮件投递、用户回传收件链接、浏览器设置新密码、旧会话/令牌/密码拒绝、新密码登录、过期及重放拒绝。密码重置开关启用后重启读回通过，临时账号已停用并释放测试邮箱绑定，审计记录保留。完整证据与边界见[身份邮件与密码恢复验收](identity-mail-acceptance-2026-10-29.md)。SMTP 凭据仅保存在 root-only 运行配置，缺少中继时配置校验仍拒绝启用重置开关。
