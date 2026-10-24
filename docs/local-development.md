# 本地开发

当前提供 Go API、八页用户中心、服务端 OIDC 会话、账户与订阅数据库、Formance Ledger 和真实 Casdoor 身份服务。浏览器从 `http://localhost:8088` 使用 `/auth/login` 登录；产品采用独立 Bearer 客户端。

独立产品可完全使用 JSON API，不需要用户中心或登录页面。注册、认证、产品权限、SDK、新产品登记和纯 API 验收见[独立产品接入](headless-product-api.md)。`PUBLIC_URL` 留空时不挂载示例用户中心；公开机器契约始终可通过 `/openapi.yaml` 读取。

## 环境与启动

需要 Go 1.26.5 或更高、Docker（含 Compose v2）、Python 3、Node.js 22 或更高（用于品牌前端语法校验）。宿主机运行 Go，依赖运行在 Docker 中。

```sh
make dev-setup
make migrate
make web-build
make api
```

`make api` 使用 `go run ./cmd/unihub serve`，同时运行恢复 worker。如需独立的恢复进程，可另开终端执行 `make worker`。`make build` 生成 `bin/unihub`，`make test` 执行 Go 测试。

完整集成验证使用 `make test-integration`：从本地配置读取测试依赖，启用 race 检查并执行真实 PostgreSQL/Formance 测试。普通 `make test` 没有显式数据库/账本测试变量时会跳过对应集成测试。业务 API 契约见[账户与资金 API](account-wallet-api.md)。

| 服务 | 固定镜像版本 | 宿主机地址 |
| --- | --- | --- |
| PostgreSQL | `postgres:16.15-alpine`，Compose 同时固定 digest | `127.0.0.1:55438` |
| Formance Ledger | `ghcr.io/formancehq/ledger:v2.4.16`，固定 digest | `http://127.0.0.1:53068` |
| Casdoor | `casbin/casdoor:v4.23.0`，固定 digest | `http://localhost:58000` |
| UniHub API | 本仓库 Go 源码 | `http://127.0.0.1:8088` |
| 临时 PKCE callback | `make dev-login` 创建 | `http://localhost:8089/callback` |
| 身份 API 文档 | 与身份服务同源，保留上游 API 定义 | `http://localhost:58000/swagger/index.html` |

所有容器发布端口都绑定 `127.0.0.1`。内部网络名为 `unihub-dev`，PostgreSQL 持久卷为 `unihub-dev-postgres`；两者在 Compose 中声明为 external，由初始化脚本在需要时创建。数据库分别为 `unihub`、`ledger`、`casdoor`；Casdoor 使用独立数据库角色 `casdoor`。

`make dev-setup` 可重复执行：保留原配置与密码、创建缺少的数据库、复用现有同名容器、启动缺少的服务。当本项目 Compose 管理的 Casdoor 容器缺少品牌前端只读挂载时，仅重建该服务容器；保留数据库、配置、密钥及日志卷，PostgreSQL/Ledger 仍复用。脚本等待 Formance 信息接口及 Casdoor discovery 就绪；逻辑账本 `unihub` 由 Go 服务启动时创建/读回。

## 本地配置与凭据

`make dev-prepare` 只生成本地配置，不启动服务。生成位置均被 `.gitignore` 忽略：

- `.local/dev.env`：API 与 Compose 环境，含数据库与 sandbox 签名密钥。
- `.local/credentials.json`：两个普通测试用户 `alice`、`bob` 的随机密码、Casdoor 管理员密码和应用 client secret。用本地编辑器查看；初始化过程不在终端打印秘密。
- `.local/casdoor/conf/app.conf`：PostgreSQL、issuer、管理员初始化密码。
- `.local/casdoor/conf/init_data.json`：`unihub` 组织、`app-unihub` 应用和测试用户。
- `.local/casdoor/web`：从固定镜像准备的 UniHub 品牌前端，只读挂载；不含认证秘密。
- `.local/access-token`：完成 PKCE 登录后保存的短期 access token。

`.local` 目录仅当前用户可访问；Casdoor 配置目录/文件额外允许当前用户组读取，通过容器 supplemental group 支持镜像 uid 1000。不要复制这些文件进版本库。

环境文件可直接用于 shell：

```sh
set -a
. ./.local/dev.env
set +a
```

不要删除 `.local` 后继续复用原数据卷：数据库原密码和 Casdoor 原应用密钥不会随配置文件重新生成而同步更换。遗失配置时应先从本地备份恢复。`.env.example` 只说明字段，不包含可用秘密。

Casdoor 应用配置为非共享应用、`JWT-Standard`、`RS256` 证书、client ID `unihub-local`、一小时 token，有且仅有 `authorization_code` grant。登录表单明确包含可见的 `Username`、`Password`、`Login button` 三项；Casdoor 只会在 `signinItems` 整体为空时补默认项，部分配置不会自动补提交按钮。全局 issuer 为 `http://localhost:58000`，发现端点为 `http://localhost:58000/.well-known/openid-configuration`。`initDataNewOnly=true` 保留数据库里已有对象，重启不会覆盖管理员控制台修改；seed 修改需要在控制台明确应用或另行迁移。新空库的 `cert-built-in` 由 Casdoor 生成新密钥。

初始化脚本包含一次窄范围兼容修复：原始 `app-unihub` 的非空登录项列表缺少 `Login button` 时追加可见按钮，同时修复本地 seed；已有该行时不操作，不覆盖显式隐藏按钮的设置，不变更账号、密码或密钥。

品牌更新使用限定列的幂等迁移：`app-unihub`、`app-built-in` 及其组织显示为 UniHub，使用本项目 Logo、favicon、页脚与青色主题。原上游默认头像/主页按已知默认值更新，自定义值保留。详见[品牌规范](../branding/README.md)。

`make test-branding` 运行无 Docker 依赖的品牌发布回归；完整镜像验证使用 `python3 scripts/prepare-branding.py --check --json`。API 文档和应用前端分别只读挂载，品牌升级需要补充挂载时会重建身份容器并结束其浏览器登录会话，数据库和密钥仍保留。品牌复核及全新身份数据库验证见[三轮审查记录](branding-audit-2026-10-24.md)。

## 浏览器 PKCE 登录及 API 调用

保持 API 运行，在另一终端执行：

```sh
make dev-login
```

该命令启动只监听 loopback 的临时 callback，创建随机 state、PKCE verifier/S256 challenge，然后打开 UniHub 登录页。在页面使用 `.local/credentials.json` 中的 Alice 或 Bob 账号。浏览器完成登录后，脚本严格检查 callback state 和 issuer，将一次性 code 与 verifier 发送至 token endpoint；仅保存响应的 **access token**。它会尝试访问 `/v1/me`，运行中的 API 拒绝 token 时不会保存；API 未启动则明确报告验签验收仍未完成。脚本不把 token 放进浏览器页面或终端输出。

这个 helper 是本地 CLI 流程，client secret 留在本地进程；它不是 BFF，不建立浏览器业务会话，不使用 implicit/password grant，不启用可信用户 header。PKCE 回调等待五分钟。再次登录可覆盖本地短期 token；切换账号时先退出 Casdoor 登录会话，或在另一浏览器会话中打开授权链接。

可以用 Python 发请求，避免 token 出现在命令行参数里：

```sh
python3 - <<'PY'
import json
from pathlib import Path
from urllib.request import Request, urlopen
token = Path('.local/access-token').read_text().strip()
request = Request('http://127.0.0.1:8088/v1/me',
    headers={'Authorization': 'Bearer ' + token})
with urlopen(request) as response:
    print(json.dumps(json.load(response), ensure_ascii=False, indent=2))
PY
```

API 验证签名、issuer、audience、有效期、非空 subject、`openid` scope，且要求 `tokenType=access-token`。ID token 和 refresh token 不能用于 API。生产启动必须使用 HTTPS OIDC 和非 sandbox 配置；本地 HTTP 例外只用于 loopback 开发。

## 管理容器与数据

`make deps-status` 查看状态；`make deps-stop` 只停止 Compose 管理的容器并保留数据。历史手工启动的同名 PostgreSQL/Ledger 容器可以被 `dev-setup` 安全复用，但不属于 Compose 项目，`docker compose up` 不能直接收编。

首次从旧手工容器迁移到 Compose 时，先保存 `.local`，结束正在运行的本地验证，再明确执行：

```sh
docker stop unihub-dev-ledger unihub-dev-postgres
docker rm unihub-dev-ledger unihub-dev-postgres
make dev-setup
```

这些命令只删除已停止的本项目容器，保留 `unihub-dev-postgres` 命名卷。脚本不自动执行这一迁移。不要执行 `docker volume rm`，也不要把本地配置作为生产部署方案。

在完成初始化后、容器已归 Compose 管理时，可使用：

```sh
docker compose --env-file .local/dev.env up -d
docker compose --env-file .local/dev.env ps
```

不要使用会展开全部秘密的 `docker compose config` 输出；校验配置使用 `docker compose --env-file .local/dev.env config --quiet`。

## 本次验证记录与边界

2026-10-24 已实际执行初始化脚本，复用原 PostgreSQL 16.15 / Ledger v2.4.16 容器，启动固定 v4.23.0 Casdoor，未停止或删除已有容器。Casdoor Docker health 为 healthy；Formance `/_info` 返回 v2.4.16；discovery 返回预期 issuer、授权/token/JWKS URL 和 S256 支持。SQL 读回确认应用只启用 authorization code、token format 为 JWT-Standard，Alice/Bob 均为非管理员且密码已散列，Casdoor 数据库 owner 为独立角色 casdoor。

后续真实浏览器验收发现初始 seed 漏掉 `Login button`，登录页只有输入框；此前数据库与健康检查没有覆盖这一用户流程。已修复生成脚本及既有应用，并确认 client secret、测试用户密码和证书私钥的前后指纹一致。隔离临时目录中实际生成新 seed、重复 prepare 均通过，按钮项可见；当前 seed/数据库补丁重复执行也通过。

修复后已完成真实浏览器链路：在 Casdoor 以 Alice 输入账号密码并点击新增登录按钮，callback 收到 `code`、`state`、`iss`，helper 使用 code + PKCE 换取 access token 并写入本地文件；真实 Go API `/v1/me` 返回 HTTP 200，用户名称为 Alice。验收截图保存在忽略目录 `.local/login-acceptance.png`。这是实际 Casdoor 登录及 API 验签验收，不是测试 issuer 的替代结果。

Compose 新空卷全栈启动及历史容器迁移尚未执行；当前验证复用了已有 PostgreSQL 卷与 Ledger 服务。订阅与业务前端的新增验收见[阶段记录](subscriptions-credits-validation.md)。

## 官方依据

- [Casdoor v4.23.0 初始化模板](https://github.com/casdoor/casdoor/blob/v4.23.0/init_data.json.template)与 [app.conf](https://github.com/casdoor/casdoor/blob/v4.23.0/conf/app.conf)：核对 seed 字段；脚本主动缩减 grant，仅创建本地测试账号。
- [Casdoor Dockerfile](https://github.com/casdoor/casdoor/blob/v4.23.0/Dockerfile)：uid 1000、镜像内 curl、`/api/health`。
- [Casdoor v4.23.0 默认登录项](https://github.com/casdoor/casdoor/blob/v4.23.0/object/application_util.go#L65-L163)及 [LoginPage 按钮分支](https://github.com/casdoor/casdoor/blob/v4.23.0/web/src/pages/auth/LoginPage.tsx#L1491)：数据库字段名称为 `Login button`，界面翻译名称为 Signin button。
- [Casdoor 数据初始化](https://casdoor.ai/docs/deployment/data-initialization/)及 [PostgreSQL 配置](https://casdoor.ai/docs/basic/server-installation/)。
- [Casdoor OAuth code/PKCE](https://casdoor.ai/docs/how-to-connect/oauth/)及 [标准 discovery](https://casdoor.ai/docs/how-to-connect/oidc-client/)。
- [Formance v2.4.16 README](https://github.com/formancehq/ledger/blob/v2.4.16/README.md)与 [Compose 环境变量](https://github.com/formancehq/ledger/blob/v2.4.16/docker-compose.yml)：Ledger 本地依赖 PostgreSQL。官方生产部署支持范围另见该 README 的 operator 说明。

## 产品客户端与用户中心

`PUBLIC_URL` 必须与浏览器实际访问 origin 一致；使用 `localhost:8088`，不要在 Cookie 会话中混用 `127.0.0.1:8088`。生产须 HTTPS。控制台密钥仅在服务端，Cookie 写操作同时校验 Origin 与 CSRF。

`OIDC_PRODUCT_CLIENTS=superwagie=superwagie-unihub-local` 为产品配置独立 audience。产品只能读取身份、账户、目录、订阅和额度，签发归属于自身产品的模型凭据；财务操作和成员管理转至 UniHub 控制台。audience 不可与控制台或运营重叠。

本地初始化另外创建 `app-superwagie`，仅登记 `http://127.0.0.1:57991/api/platform-account/callback` 与 `http://localhost:57991/api/platform-account/callback`。随机 client secret 仅存 Casdoor/本地私有配置，绝不交给 SuperWagie。固定 Casdoor v4.23.0 支持带有效 S256 challenge/verifier 的无 secret code 换取；保留随机 secret 可避免无 PKCE 的空 secret 匹配。见[对应源码](https://github.com/casdoor/casdoor/blob/v4.23.0/object/token_oauth.go#L159-L277)。现有 client 配置不会被重复初始化覆盖。

SuperWagie 使用独立的 `OPENCHAMBER_UNIHUB_*` 配置；登录令牌保存在后端最多一小时的内存会话，浏览器/桌面渲染器不持有平台 access token。重启后重新登录，当前没有长期 refresh token 或隐式自动登录。
