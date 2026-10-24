# UniHub 发布

首版使用 `v0.1.0`，定位为独立后台 API 的沙盒预览。Web、SDK、OpenAPI 与根目录 `VERSION` 保持相同版本。真实支付未配置、交互式 MFA 尚未续接及产品直接充值统一钱包未开放等边界必须保留在发布说明中。

## 构建文件

先更新 `VERSION`、`CHANGELOG.md`、必要版本号并完成验证，提交后再打包。打包工具要求干净工作区，锁定当前提交及提交时间；未提交的修改不能混入附件。需要 Go 1.26.5+、Python 3、Node.js 24+，并已根据 `web/package-lock.json` 安装前端依赖。工具不会安装依赖、推送 Git、创建 Release 或部署服务器。

```sh
npm --prefix web ci
python3 scripts/package-release.py \
  --casdoor-web .local/release-inputs/casdoor-web \
  --identity-artifacts .local/casdoor-audit-build/image-context
```

默认读取 `VERSION`，输出到新目录 `.local/releases/v<VERSION>`。可使用 `--version` 校验显式版本、`--output` 指定另一个新目录。已有目录不会被覆盖。Go 服务及受控身份验收工具交叉编译为 Linux amd64，`CGO_ENABLED=0`；示例前端执行正式构建。

`--casdoor-web` 是显式提供的可信公共静态资源目录，只包含 Casdoor 前端和 Swagger。工具仅对临时副本应用/验证受控同域回调补丁，保留原输入，检查 bootstrap 必需的入口、品牌资源及补丁清单。省略时服务包仍可构建，但没有完整身份前端，部署前必须另外准备；不可把不完整服务包直接描述为可独立启动的全部依赖。

`--identity-artifacts` 只接受审查过的 `image-context`：Linux amd64 二进制、Dockerfile、`.dockerignore`、Apache LICENSE、修改 NOTICE 与来源清单。工具核对源码锁、补丁哈希、二进制哈希、ELF架构及脱敏回归标志。该参数生成独立的后端构建上下文附件，不把实际镜像ID或审计启用状态从一台服务器复制到另一台。

输出包含：

- `unihub-<version>-linux-amd64.tar.gz`：服务二进制、可选用户中心、部署工具、文档、品牌许可和可选身份静态资源。解压时去掉首层目录，作为新的不可变 release 使用。
- `unihub-sdk-<version>-source.tar.gz`：`packages/sdk/` 的私有工作区 TypeScript 源码及接入文档。它不是 npm 发布包；由接入产品构建工具编译源码，不承诺在解压目录直接运行依赖原仓库路径的 typecheck。
- `unihub-casdoor-audit-<version>-linux-amd64.tar.gz`（可选）：带原始 LICENSE、修改 NOTICE、来源清单、源码锁及补丁的后台 Docker 构建上下文。
- `unihub-<version>-openapi.yaml`、`release-manifest.json` 和 `SHA256SUMS`：机器契约、源提交/版本/工具链/文件校验以及所有附件的SHA256。

所有 tar 路径、成员与内容哈希在生成后重新读取核验；归档的排序、时间、所有者与权限已规范化。保留相同提交、工具链和公共输入时可复现附件。前端构建拒绝本机 `web/.env*` 运行配置，忽略环境中的 `VITE_*`，避免无意把本机后端地址或凭据编进前端。

打包使用受控文件清单，不复制整个 `.local`；运行环境文件、生产 client policy、数据库与用户、证书私钥、token、`node_modules`、链接和特殊文件均不进入附件。输入公共资源树若混入这些内容，打包会拒绝。SHA256SUMS 包含本次所有其他附件与发布清单，可在下载后执行：

```sh
sha256sum -c SHA256SUMS
```

## 标记、上传与部署

核对附件后，在源提交上建立 `v<VERSION>` 标签并推送到指定仓库；发布说明引用该标签、改动和验收边界，上传上述附件。UniHub 仓库已公开；SDK 的 `private:true` 仍用于防止意外发布至 npm。不在 SDK npm registry 或容器 registry 执行隐含发布。GitHub 自动生成的源码压缩包不包含被忽略的本地构建和凭据。

服务器升级沿用[部署说明](../deploy/server/README.md)：升级前备份与恢复核验；将服务包解压到新的 release，校验 `release-manifest.json`；保持本安装的私有配置和账户；使用目标机实际镜像ID登记带来源清单的身份后端；先保持身份gate关闭，再完成实际审计/日志验收后启用。不可把发布包中不存在的真实支付、模型供应商或第三方连接说成已配置。

将新 `current` 原子切换并重启本项目服务后，复核 HTTPS、公开 OpenAPI、独立产品身份/资源流程及原有数据。数据库迁移可能只向前，回滚二进制不代表回滚数据；对应备份、旧发布和校验清单一并保留。
