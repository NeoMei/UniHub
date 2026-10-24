# UniHub 首阶段服务器部署验收

日期：2026-10-24（Asia/Shanghai）。入口：**https://unihub.quukk.com**。部署为持续运行的首阶段测试环境，页面明确标注“不涉及真实支付”；未配置真实支付商户、模型供应商或迁移其它产品账户。

## 服务与隔离

- 公网 HTTPS443 转发到目标服务器8484，由现有 nginx 和有效的 `*.quukk.com` 证书提供 TLS。按用户最终决定仅支持 HTTPS；此域名 HTTP80 专属 vhost 返回444，不回落到同机默认站点。
- 目标主机：`113.249.120.24`，Ubuntu24.04 x86_64。程序位于 `/opt/unihub/releases/20261024-8c6d526`，`/opt/unihub/current` 指向当前 release。
- `unihub.service` 使用独立非 root 用户，已启用开机启动与失败自动重启；只监听 `127.0.0.1:8088`。现有站点配置和应用未替换，llmrouter 原 HTTPS 入口继续返回200。
- 三个容器为 `unihub-server-postgres`、`unihub-server-ledger`、`unihub-server-casdoor`。PostgreSQL16.15 / Formance2.4.16 / Casdoor4.23.0 使用本仓库固定 digest；独立网络、持久卷、三数据库和各自所有者，端口55438/53068/58000仅绑定 loopback。
- 服务器重新生成全部数据库、身份、模型服务和测试支付密钥；未复制本机测试账户、令牌或账本。重复 bootstrap 后凭据指纹与用户 ID 不变。私有配置为 root0600，父目录0700。
- 当前共享证书有效至2026-12-15；此次复用现有证书，未改其续期安排。部署工具及日志检查没有输出私钥、密码、令牌或 OAuth code。

## 真实验收

| 验证 | 结果 |
| --- | --- |
| HTTPS / 资源 / OIDC discovery | 公网200，issuer 为 `https://unihub.quukk.com`；主页面、console资源与两套品牌资源正常 |
| 登录 | 验收账户真实密码登录及已有身份授权、Owner真实密码登录，经PKCE/state/nonce/签名校验进入Go用户中心 |
| 会话保护 | UniHub及Casdoor Cookie具备 Secure、HttpOnly、SameSite=Lax；本站访问日志不含query，错误日志不记录回调参数 |
| 入口限制 | `/api/signup`403；`/internal/v1/rates`及`/.env`404；服务器本机HTTP请求直接关闭连接 |
| 测试充值与会员 | 独立 acceptance 账户创建并确认10元模拟充值，购买1元沙盒月度基础会员，订单已付款/权益已发放 |
| 真实账本读回 | SuperWagie产品现金 `9000000` CNY/6，模型批次可用 `1000000` CREDIT/6，冻结和消费均为零；测试商品不代表正式价格 |
| 团队隔离 | 新建“服务器验收团队20261024”，独立会员0、额度0、账单空；切回个人仍为1个会员及1.000000额度 |
| 服务重启 | 重启 `unihub.service` 后浏览器原会话仍有效、会员/账单/额度完整，未重复发放 |
| 备份与恢复 | 两次在线物理全集群备份覆盖三数据库和所需WAL；目标机固定PG镜像在无网络、无端口、随机新卷恢复，通过checksums、pg_verifybackup、数据库owner与签名密钥存在性检查；测试容器/卷已清理 |
| 每日备份 | `unihub-backup.timer` 已启用，上海时间每日03:30加不超过5分钟随机延时；实际调用同一systemd备份service成功，结果0 |

验收数据只存在独立 acceptance 用户；交付用 `owner` 个人账户保持零余额、零会员。首次密码已保存到本机忽略目录 `.local/server-deployment/owner-login.json`（0600），服务器完整私有凭据在 `/opt/unihub/shared/credentials.json`；本文不包含秘密。公共注册初期关闭，普通账户与内置身份管理员分开。

截图与机器证据在本机忽略目录 `.local/server-deployment/`，包括 `acceptance-overview.png`、`team-isolation.png`、`persistence-after-restart.png` 和 `owner-overview.png`。站点没有执行真实收款或真实模型调用。备份恢复验证目前覆盖数据库与身份密钥，未执行整套应用在异地恢复后的登录/业务演练；备份保留在本机私有目录，未配置异地复制或自动删除。

## 审查修复与实施裁定

1. 新版 Docker 对缺失 volume 返回小写 `no such volume`，已兼容真实错误格式，权限/daemon错误仍拒绝。
2. Casdoor 内置应用默认允许注册，启动SQL窄范围关闭并读回；nginx先行拒绝 `/api/signup`，保护首次启动到SQL执行之间的窗口。业务应用 seed 同样禁注册。
3. nginx 默认错误日志可能记录 OAuth query，本站改为无query的独立访问日志并禁用包含request的本站错误日志；应用journal仍可诊断。
4. Casdoor经HTTP内部代理时Cookie可能缺Secure，本站代理补齐安全标志，真实响应已确认。
5. Casdoor4.23.0把同origin回调当成自身页面，将code改为普通login。部署专用资源补丁仅让精确的HTTPS `/auth/callback` 维持授权码流程，其它URL和Go认证校验保留。源main SHA256 `551d29642ebe1dd4afb6821f56ece30ba74ba5d4104b772d332408f1205fd2db`，输出SHA256 `7524cc6466af1e97011ec846cc19089c7f5d39dbdcd2bb17f4a3d8d0b9f167d2`；完整资源图换名、旧资源保留、独立manifest及幂等校验。已通过8项测试、219个新JS模块语法检查、独立审查及真实公网登录。[上游self-redirect逻辑](https://github.com/casdoor/casdoor/blob/v4.23.0/web/src/lib/setting.tsx#L79-L95)、[登录请求类型选择](https://github.com/casdoor/casdoor/blob/v4.23.0/web/src/pages/auth/LoginPage.tsx#L565-L577)。
6. 第一阶段保留显式 development+sandbox 配置和 loopback 约束，通过 HTTPS 反向代理提供可见测试环境；没有放宽production禁止sandbox的规则。若转真实商用，需另配置支付方式与正式商品并完成验收。
7. 服务器下载镜像慢，改为本机按相同固定digest取linux/amd64镜像、导出上传后恢复官方digest关联；目标机确认固定digest及架构。未改服务器全局镜像源。
8. UniHub保持现有专用feature工作区，部署新增代码只在本仓库；所有远程操作限定新建UniHub资源，未使用用户提供的密码（既有SSH身份已可连接），未把服务器凭据写入代码或文档。

部署工具通过11项测试（含真实nginx TLS/Cookie/日志/HTTP拒绝验证），同域身份资源补丁8项测试；Linux二进制与TypeScript/Vite构建成功。本轮未修改Go账务逻辑，沿用此前真实PG/Formance race验收。

升级、备份、回滚及初始账户操作见 [服务器操作说明](../deploy/server/README.md)。数据库迁移可能只向前，回滚二进制不等于回滚数据。部署源及发布包应一起保留。

## 管理员与普通用户测试入口

后续按用户要求交付两个既有账号：身份管理后台 `built-in/admin` 与业务普通用户 `unihub/owner`，没有重置密码或扩大普通用户权限。管理员使用 `https://unihub.quukk.com/login/built-in?from=%2Fusers&silentSignin=1`，普通用户使用主站。初始密码保存在本机忽略目录 `.local/server-deployment/test-accounts.json`（0600），不写入本文。

已补齐 nginx 对 `/users`、`/organizations`、`/applications` 及其子路径的身份前端分流；原认证、Cookie、禁注册及内部接口限制保留。11项部署测试通过（含真实nginx路径及相近非匹配路径验证）。真实HTTPS接口验证 admin 登录、isAdmin=true且可读用户列表；owner 登录、isAdmin=false且用户列表请求被服务端拒绝。真实浏览器 admin 密码登录进入用户列表，刷新仍可用。普通会话访问同页显示未授权，不能因换登录URL自动获得管理员身份。建议两个角色使用分开的浏览器会话进行对照测试。

此入口为身份管理后台；UniHub 独立的计费运营可视化后台仍在后续范围。
