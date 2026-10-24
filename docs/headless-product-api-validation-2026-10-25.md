# 独立产品 API 验收

日期：2026-10-25（Asia/Shanghai）。UniHub 按用户要求提供独立后台服务：产品拥有全部 UI 和业务功能判断，注册、认证、充值、权益和资源读取使用 JSON HTTP。示例用户中心不构成依赖。

## 已验证

- 真实 PostgreSQL/Formance 隔离 schema/逻辑账本上，全仓库 race 回归：13 个测试包、301 个测试及子测试通过，无失败、无测试用例跳过。另有三个包没有测试文件。`go vet ./...` 通过。
- 真实 Casdoor 4.23.0 + UniHub 可执行程序 + Formance，通过 `scripts/headless_e2e.py` 完成 JSON 注册与同 key 重放、密码授权、S256 PKCE、令牌交换和重放拒绝；全程加载 HTML 页面数为零。
- 充值同 key 返回原订单，显式 sandbox 到账后账本操作为 APPLIED；购买测试月度基础套餐后现金为 `9000000` CNY/6，共享模型额度为 `1000000` CREDIT/6。
- 独立 llmrouter 服务身份执行受控模型调用预占/结算，输入3、输出4的测试费率产生4个最小额度单位，消费后 CREDIT 可用 `999996`；现金保持 `9000000`。没有调用真实模型或支付渠道。
- 产品 token 读取调用分页、明细和完整汇总，来源固定为产品，settled_calls=`1`、charged=`4`，不返回 provider_cost。
- 团队创建、收件邀请列表、本人接受、成员查询都通过；普通成员财务读取、非成员账户读取、未注册 Origin 均被拒绝。
- TypeScript SDK 8 项真实 HTTP 协议测试及严格类型检查通过；另以真实 Casdoor 登录，读 me/accounts/resources 并清除内存 token，通过实际接入验收。
- 新产品登记工具6项回归及真实 Casdoor创建/读回通过；重复 prepare/apply 没有第二次写入。独立 product/client/application 无需修改 Go 代码。
- 公开 `/openapi.yaml` 实际读取为 OpenAPI3.1.0；契约含33个路径、38个操作与35个结构。SDK及契约没有可信模型结算的客户端能力。

首次全流程运行发现本地验收工具未把临时 schema 编入子进程 DSN，误用了本地开发 schema；已修正为显式 search_path 后重新验证隔离 schema/账本。首次创建的独立普通测试用户及沙盒账务对象留作开发审计记录，未修改原测试用户或生产账户。该次失败未被作为通过证据。

## 审查与修复

独立代码审查覆盖令牌绑定、scope与角色的联合校验、跨产品资金命令、CORS、历史调用时间和游标。修复了代理后共用IP限流、超限IP仍增长用户名桶、SDK接受远程明文HTTP以及运行时能力边界未声明四项问题；真实数据库回归证明不同真实IP互不继承限制，超限请求不能继续插入新用户名桶。真实IdP验收额外发现固定版本的授权码为64位十六进制，已同步适配器、SDK及契约。

服务端密码及PKCE校验不由前端按钮决定。产品获得 scope 后仍须当前账户权限；可信模型预占与结算只接受独立 llmrouter 服务身份。用户中心的 Cookie/CSRF 保护保留。

## 明确边界

当前真实支付渠道、正式套餐和真实模型供应商尚未配置。测试充值与测试套餐不能视为真实商用到账。产品 token 可充值获准产品钱包；直接充值统一钱包暂不开放，运行时 capabilities 会声明。统一钱包的显式划拨及授权仍保留。

MFA、CAPTCHA、强制改密等交互返回 interaction_required，尚未实现交互续接；没有公开 refresh 或服务端 logout API。SDK clearAccessToken 只清内存。查询是实时快照，进程重启后的旧游标失效；历史缺失时间不补造。

Casdoor审计补丁只扩展固定版本的敏感字段名单，不改认证逻辑。受控适配器发送规范编码，需真实服务审计/日志核验后才可开启部署gate。它不声称覆盖直接调用Casdoor的任意等价转义字段。未配置外发审计sink时，验收会明确记录none_configured，并结合上游实际函数的sink前脱敏回归；未来配置外发sink必须再做实际接收端验收。

可重放工具与接入方法见[接入文档](headless-product-api.md)。本地原始测试证据存放在被忽略的 `.local/` 目录；密码、令牌、DSN和私钥不进入本报告或Git。

## 服务器升级

升级前完整物理备份：`/opt/unihub/backups/20261024T163918Z`；已通过真实固定PG镜像隔离恢复、checksums、pg_verifybackup、三个数据库owner及身份签名密钥存在性验证。完整异地应用恢复仍不在此次证明范围。

已部署至 `/opt/unihub/releases/20261025-headless-v1`，`current` 已切换，`unihub.service` 及三依赖健康。实现版本为 `5c9d4c4`，公网私有路径拒绝断言修正为 `5a38aab`。公开机器契约：[OpenAPI](https://unihub.quukk.com/openapi.yaml)。

Casdoor在目标机按实际不可变镜像ID登记：`sha256:6e8d821320319a0034dc610eaaceb7180ec658a2bee7d97afaf64e441211d66c`，`/server` SHA256为 `3f385dc9a19577835113fcbab5843bd0053207f315ee787e9749bbe0ac90e5d9`；没有把本地镜像冒充已发布仓库digest。严格校验归属、镜像和运行二进制后，只对规范身份适配器路径核验真实审计/日志，才开启注册与认证。

已登记独立客户端 `superwagie-headless`（应用 `app-superwagie-headless`），精确回调为 SuperWagie 现有loopback路径；同产品旧客户端保持原权限。新客户端获准自身及llmrouter商品/钱包，仍须当前账户角色。该登记不表示SuperWagie已替换其现有接入实现。

通过公网HTTPS完成相同的JSON注册→认证→充值→购买→团队→资源查询流程，并由独立受信llmrouter身份在loopback执行合成模型预占和结算。结果为现金 `9000000`、额度 `999996`、1个已结算调用和收费 `4`，HTML页面加载为零。公网SDK另从本机实际认证并读回一致结果。公网私有结算路由由nginx拒绝；首次脚本把这个HTML拒绝页误判为JSON协议失败，已修正为仅接受预期错误断言的代理拒绝，所有成功产品请求仍必须为JSON。仅清理该次合成验收来源的两条注册限流桶后重新验收，没有关闭或放宽运行时限流。

重启 `unihub.service` 后，原access token继续通过验证，资金/额度读回一致且未重复发放；升级前备份中的全部原有凭据逐项一致。普通owner账号与身份admin账号未重置。新增用户与财务对象均为独立合成测试账户，原交付owner账户保留。

外发Webhook/Audit provider当前均未配置；报告明确标记none_configured+source_function_test，未声称验证了真实外部接收端。真实审计表、本站Nginx、journal、Docker输出及Casdoor文件日志均检查合成密码、令牌与PKCE标记。公网入口仍只支持HTTPS，内部路由和直接Casdoor signup仍被拒绝。

服务器聚合证据位于 `/opt/unihub/shared/headless-acceptance-20261025/` 的公开结果JSON，目录受root权限保护；私密标记文件不作公开交付。本机无秘密副本在 `.local/server-deployment/headless/`。
