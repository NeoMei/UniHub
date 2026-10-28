# SuperWagie 生产接入准备（UniHub 线上）

日期：2026-10-29。状态：本地配置与手册已就绪；线上尚未合并，Casdoor 浏览器客户端尚未注册。本文是切换手册，执行前先复核线上现状。

范围：仅 SuperWagie 生产接入 UniHub。MFA、运营后台和 SDK 发布不在本手册范围内。

## 现状（2026-10-29 读回）

- 线上 `/opt/unihub/billing-config/product-runtime.env` 的 `PRODUCT_RUNTIME_SERVICES_JSON` 只含 `llmrouter`。
- `PRODUCT_USER_MANIFESTS_FILE=/opt/unihub/headless-config/product-user-manifests-v3.json` 含 `llmrouter`（v3）与 `notes`（v1），无 `superwagie`。
- `PRODUCT_CLIENTS_FILE=/opt/unihub/headless-config/product-user-clients-v3.json` 已登记 `superwagie-headless`（headless JSON 登录/注册客户端，计费产品为 `superwagie`、`llmrouter`）、`llmrouter-headless`、`llmrouter-browser`。
- 服务由 systemd 管理：`unihub.service` 加载 `/opt/unihub/shared/runtime.env`，drop-in `/etc/systemd/system/unihub.service.d/product-runtime.conf` 追加加载 `/opt/unihub/billing-config/product-runtime.env`；当前 release 见 `/opt/unihub/current`。

## 配置模型要点

- `PRODUCT_RUNTIME_SERVICES_JSON`：产品→UniHub 的服务通道。键为服务名；每项含 `token`（≥32 字节、无空白、全局唯一且不等于 `MODEL_SERVICE_TOKEN`）、`product_id`、`instance_id`、`allowed_ops`。`allowed_ops` 必须落在服务端白名单内，且 `(product_id, instance_id)` 必须已出现在 `PRODUCT_USER_MANIFESTS_FILE` 中，否则启动失败。
- `PRODUCT_USER_MANIFESTS_FILE`：产品实例声明（名称、入口、版本、角色与默认角色）。默认角色不得是特权角色。
- 任何带 `access:read` scope 的客户端都要求对应产品实例有 manifest。
- OIDC 客户端通过 `scripts/product_client.py` 的 prepare/apply/readback 流程在 Casdoor 登记为受管应用，登记后原子发布客户端注册表。

本地产物（不含真实密钥，token 为占位符）：

- `config/superwagie/product-runtime-services.json`：`superwagie` 服务项，`allowed_ops` 对齐 llmrouter 清单并去掉 `credits:payment`（SuperWagie 不处理支付回调），保留 `access:read`。
- `config/superwagie/product-user-manifests.json`：`(superwagie, default)` manifest，`member` 默认角色仅 `product:use`。

两份文件已用线上同一解析器验证：单独解析通过，且与生产 v3 manifests 合并后解析通过。

## 第一步：在 Casdoor 注册 SuperWagie PKCE 浏览器客户端

前置：在受审 UniHub checkout 上操作；准备好私有管理客户端凭据（`HEADLESS_MANAGEMENT_CLIENT_ID`/`HEADLESS_MANAGEMENT_CLIENT_SECRET`，仅经环境变量提供，绝不写入文件或提交）；确认生产 Casdoor 组织 `unihub` 策略可过 preflight。

```sh
WORK_DIR=.local/superwagie-onboarding   # 忽略目录，勿提交
mkdir -p "$WORK_DIR"
scp root@113.249.120.24:/opt/unihub/headless-config/product-user-clients-v3.json \
  "$WORK_DIR/product-user-clients-v3.json"

python3 scripts/product_client.py prepare \
  --registry "$WORK_DIR/product-user-clients-v3.json" \
  --output "$WORK_DIR/superwagie-browser-plan.json" \
  --product-id superwagie --instance-id default \
  --client-id superwagie-browser --application app-superwagie-browser \
  --redirect-uri http://127.0.0.1:57991/api/platform-account/callback \
  --redirect-uri http://localhost:57991/api/platform-account/callback \
  --origin http://127.0.0.1:57991 \
  --origin http://localhost:57991 \
  --scope access:read --scope identity:read \
  --scope profile:write --scope accounts:read --scope accounts:write \
  --scope members:read --scope members:write --scope catalog:read \
  --scope subscriptions:read --scope credits:read --scope wallets:read \
  --scope recharges:read --scope recharges:write --scope transfers:write \
  --scope operations:read --scope orders:read --scope orders:write \
  --scope settings:read --scope settings:write --scope refunds:write \
  --scope usage:read --scope model-credentials:write \
  --billing-product superwagie --billing-product llmrouter \
  --password-login --registration --browser-sso
```

人工审查 plan JSON（公开策略，无密钥），确认 redirect/origin/scope 与上文一致。然后：

```sh
export HEADLESS_MANAGEMENT_CLIENT_ID='...'   # 私有管理客户端
export HEADLESS_MANAGEMENT_CLIENT_SECRET='...'
python3 scripts/product_client.py apply --plan "$WORK_DIR/superwagie-browser-plan.json" \
  --issuer https://unihub.quukk.com
python3 scripts/product_client.py readback --plan "$WORK_DIR/superwagie-browser-plan.json" \
  --issuer https://unihub.quukk.com
unset HEADLESS_MANAGEMENT_CLIENT_ID HEADLESS_MANAGEMENT_CLIENT_SECRET
```

apply 只写名为 `app-superwagie-browser` 的受管应用并在读回一致后原子发布本地注册表副本；验证成功后把更新后的 `product-user-clients-v3.json` 同步回线上（见第二步）。若 Casdoor 中已存在同名应用且策略不符，apply 拒绝并要求人工审查，不得手工覆盖。

## 第二步：合并配置到线上并重启

全部在服务器上执行，先备份：

```sh
ssh root@113.249.120.24
STAMP=$(date +%Y%m%d%H%M%S)
mkdir -p /opt/unihub/backups/superwagie-onboarding-$STAMP
cp /opt/unihub/billing-config/product-runtime.env \
  /opt/unihub/headless-config/product-user-manifests-v3.json \
  /opt/unihub/headless-config/product-user-clients-v3.json \
  /opt/unihub/backups/superwagie-onboarding-$STAMP/
```

1. 生成服务 token（不回显、不入库）：

```sh
umask 077
openssl rand -base64 48 | tr -d '\n' > /root/.superwagie-runtime-token
```

2. 追加 manifest：把 `config/superwagie/product-user-manifests.json` 的数组元素并入 `product-user-manifests-v3.json`，保持其余条目原样。建议在本地合并成完整文件后 scp，服务器上先 `diff` 备份与新文件，确认只新增 `superwagie` 条目再覆盖 `/opt/unihub/headless-config/`。

3. 合并 `PRODUCT_RUNTIME_SERVICES_JSON`：在现有 JSON 上新增 `superwagie` 项，`token` 用第 1 步生成值替换占位符，`product_id=superwagie`、`instance_id=default`、`allowed_ops` 与 `config/superwagie/product-runtime-services.json` 一致。不要改动 llmrouter 条目。

4. 放入第一步 apply 发布的最新客户端注册表（若尚未同步），确认 diff 只新增 `superwagie-browser`。

5. 修正属主并重启：

```sh
chown unihub:unihub /opt/unihub/headless-config/product-user-manifests-v3.json \
  /opt/unihub/headless-config/product-user-clients-v3.json
chmod 600 /opt/unihub/billing-config/product-runtime.env
systemctl restart unihub
systemctl status unihub --no-pager
journalctl -u unihub -n 50 --no-pager
curl -fsS https://unihub.quukk.com/api/health
```

启动失败（例如 manifest 缺失或 token 校验不过）时，恢复备份文件并再次 `systemctl restart unihub`，先在本地修正再重试。

回滚：把 `/opt/unihub/backups/superwagie-onboarding-$STAMP/` 内文件复制回原位后重启；Casdoor 应用如需下线，用 readback 确认后按同一工具策略处理，不直接改库。

## 第三步：SuperWagie-open 侧配置 UniHub 端点

SuperWagie 平台账户模块（`packages/web/server/lib/platform-account/`）读以下环境变量：

| 变量 | 值 |
| --- | --- |
| `OPENCHAMBER_UNIHUB_ISSUER` | `https://unihub.quukk.com`（官方构建默认值，可不设） |
| `OPENCHAMBER_UNIHUB_API_URL` | `https://unihub.quukk.com`（同上） |
| `OPENCHAMBER_UNIHUB_HEADLESS_CLIENT_ID` | `superwagie-headless`（默认值） |
| `OPENCHAMBER_UNIHUB_HEADLESS_REDIRECT_URI` | `http://127.0.0.1:57991/api/platform-account/callback`（默认值） |
| `OPENCHAMBER_UNIHUB_CLIENT_ID` | `superwagie-browser`（第一步注册的 PKCE 浏览器客户端，需显式设置） |
| `OPENCHAMBER_UNIHUB_REDIRECT_URI` | `http://127.0.0.1:57991/api/platform-account/callback`（路径必须为 `/api/platform-account/callback`） |
| `OPENCHAMBER_UNIHUB_CONSOLE_URL` | 可选，`https://unihub.quukk.com` |

桌面端通过系统浏览器完成 PKCE 登录后回调本地后端；HTTP 仅允许回环地址。禁止设置 `OPENCHAMBER_UNIHUB_DISABLED=1` 或 `OPENCHAMBER_UNIHUB_MODE=off`。会话 token 上限 1 小时且无刷新令牌，过期需重新登录。

## 第四步：验收清单

生产切换后逐项执行并留档（真实结果、时间、账号 UUID）：

1. **登录**：桌面端发起登录打开系统浏览器，完成 Casdoor PKCE 授权并回调 `127.0.0.1:57991`，会话建立；headless JSON 登录与注册各验一次；错误密码与过期会话失败路径符合预期。
2. **账户选择**：个人/团队账户切换成功；切换时校验当前成员资格；任务启动前捕获 actor/account，不因切换改变进行中任务。
3. **会员/额度读取**：`/v1/me`、`/v1/accounts/{id}/subscriptions`、`/v1/accounts/{id}/credits` 正常；会员状态与额度（六位小数）显示正确；个人与团队数据隔离。
4. **模型扣费对账**：发送一次真实模型调用，收到 `[DONE]` 与 SETTLED 回执；UniHub 调用记录、产品回执与 Formance 账本三方一致；用精确有理数重算费用核对微额度；重复回执 ID 返回 409；错误任务不重复扣费。
5. **运行时服务通道**：确认 UniHub 日志无 product runtime 校验错误；`credits:payment` 未授予（支付回调仍归 llmrouter）。

以上任何一项失败时记录真实错误并停止扩大范围；不要用沙箱结果替代生产验收结论。
