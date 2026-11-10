# AGENTS.md — UniHub

给在本仓库工作的 AI agent。产品语义见 `CONTEXT.md`，本地环境见 `docs/local-development.md`。

本文件只管一件事：**多个客户端同时在这个仓库上开发时，怎么不互相踩。**

## 前提

UniHub 可能同时被 **Codex、犇马（SuperWagie）、WorkBuddy** 三路在各自的 worktree 上开发。三方共享的只有一份 `.git` 历史；工作区、分支、本地栈、依赖各自独立。

三方交接点是**文件**，不依赖任一客户端的 API：

- 代码交接：各仓库 `main` 分支
- 状态记录：`~/项目/codexprojects/U-L-S 综合验收/OPERATIONS.md`。**发布方追加新段落，不改旧段落**；总控保留验收段落。三方不并发写同一段落。
- 交接日志：同目录 `HANDOFF.md`。**append-only**：写入前先读文件尾部，只在末尾追加；**禁止整文件覆盖或重写模板**。该文件无技术写保护（2026-11-10 22:19 曾被整体重写丢 3 条记录），整文件写入前必须先确认没有他人条目。

## 工作区

- 用独立 git worktree，不要直接改主工作区 `~/项目/codexprojects/unihub`。
- worktree 根目录读 `~/.codex/config.toml` 的 `git-worktree-root`（当前是 `/Volumes/codex/new-worktrees`）。外置卷没挂载就停下来说明，不要静默回退到内置盘。
- 目录命名 `<用途>/unihub`，用途带所有者前缀：`codex-`、 `sw-`（犇马）、`wb-`（WorkBuddy）。例如 `wb-unihub-pilot/unihub`。
- 任务结束后用 `git worktree remove` 清掉，不要留孤儿目录。

## 分支

| 客户端 | 前缀 |
|---|---|
| Codex | `codex/*` |
| 犇马 | `sw/*` |
| WorkBuddy | `wb/*` |

- 分支前缀就是所有权，只在自己的前缀下提交。**`agent/*` 已冻结**：历史遗留分支（`agent/parallel-dev-stack` 已合入 main），不再提交、不再新建；新提交只进 `codex/*` / `sw/*` / `wb/*`。本约定立即生效，不等规则合 main。
- **三方都不直接推 `main`**，只走分支 + 由人合并。常规合并时机：每轮联调开始前，由人在 `main` 上合入已验收的分支；合完各客户端 `fetch` 后从 `main` 拉新分支。
- 人合并时在 merge commit message 末尾加一行 `merged-by: human`，用来区分「人合并」与「agent 代做」——`main` 上的历史合并提交多为项目原身份，靠作者判断不出来。2026-11-10 之前混用项目身份与 `agent@local` 的提交不追溯、不改写。
- **发布例外**：发布类合并可随时进行，不受「每轮联调开始前」限制，条件是人授权 + 在 `HANDOFF.md` 记下 tag 与 commit。进行中的分支允许 `merge main` 保持同步；**禁止跨客户端 `rebase` 他人分支**，要改造别人的分支就提出来由对方做。
- 从 `main` 拉新分支。需要别人未合并的改动时，在 `HANDOFF.md` 写明依赖的是哪个分支。

### 提交身份

分支前缀只是名义所有权，提交作者必须也能区分客户端。每个客户端在自己的 worktree 设 local 作者（不改全局配置）：

| 客户端 | user.name | user.email |
|---|---|---|
| Codex | `Codex` | `codex@local` |
| 犇马 | `SuperWagie`（或 `犇马`） | `sw@superwagie.local` |
| WorkBuddy | `WorkBuddy` | `wb@workbuddy.local` |

```sh
git config user.name "WorkBuddy"
git config user.email "wb@workbuddy.local"
```

已推送的提交不改写。历史提交沿用项目原身份，不影响本约定。

犇马的 name 写 `SuperWagie` 或 `犇马` 都可以，以 email 区分即可。`96657cc`（`sw/unihub-parallel-stack-docs`）用的是 `犇马 <sw@superwagie.local>`，符合本约定，不需要改。

## 生产边界

- 生产部署、生产库写入（含直接执行 SQL）**只由总控会话执行，且须先取得人的授权**。客户端会话不自行部署、不自行改生产数据。
- 渠道连接测试、对外探针类操作**必须写明目标环境**（本地 / 联调 / 生产），不得默认打生产。
- 允许在连接测试里直接调用收费 API——这条既有偏好不变，本约束只增加「声明目标环境」的义务。
- 生产相关的 tag、镜像、发布动作记入 `OPERATIONS.md`，并在 `HANDOFF.md` 留下 tag 与 commit。

## 本地栈

每个 worktree 一个实例名，和分支所有者一致：

```sh
UNIHUB_DEV_INSTANCE=wb UNIHUB_DEV_PORT_OFFSET=200 make dev-setup
```

偏移规则（`scripts/dev-setup.sh`）：**不设实例名 → 偏移 0；设了实例名但没给 `UNIHUB_DEV_PORT_OFFSET` → 默认 100**。从第二个实例起必须显式指定偏移，否则端口和容器名会撞。

一个实例实际占用 **6 个端口**，全部 = 基准 + 偏移：

| 端口 | 基准 |
|---|---|
| PostgreSQL | 55438 |
| Formance | 53068 |
| Casdoor | 58000 |
| UniHub API | 8088 |
| 登录回调（`make dev-login`） | 8089 |
| SuperWagie 回调 | 57991 |

当前偏移分配：

| 客户端 | 实例 | 偏移 | PostgreSQL | Formance | Casdoor | API | 登录回调 | SW 回调 |
|---|---|---|---|---|---|---|---|---|
| Codex | （默认）`unihub-dev-` | 0 | 55438 | 53068 | 58000 | 8088 | 8089 | 57991 |
| WorkBuddy | `unihub-dev-wb` | 200 | 55638 | 53268 | 58200 | 8288 | 8289 | 58191 |
| 犇马 | `unihub-dev-sw` | 300 | 55738 | 53368 | 58300 | 8388 | 8389 | 58291 |

- 偏移 100（`bb` 实例）已回收，端口段空闲；新增实例往后顺延 400 并更新这张表。
- 联调时 SuperWagie 侧必须指向同一个偏移后的 SW 回调端口。
- 不要对同一个实例名并发跑 `make dev-setup`。机制细节见 `docs/local-development.md`。
- `docker inspect <name>` 不加 `--type container` 时，容器未创建会命中同名卷；已锁定的 PostgreSQL 镜像是 linux/amd64，Apple Silicon 首次 initdb 可能超过 60 秒（等待窗已放宽至 120 秒）。

## 依赖与环境

- `node_modules`、`bin`、`.local` 不跨 worktree 共享，每个 worktree 自己准备。
- `.local/dev.env` 含本实例的密钥和端口，只属于该 worktree：不要复制到别的 worktree，不要提交。
- 不要读或改别人 worktree 下的 `.local`。

## 交接

- 收工前把改动提交到自己的分支并 push。没提交的改动对另外两方不可见。
- 接手前 `git fetch`，确认 `main` 与 `origin/main` 一致，读最近的提交说明。
- 在 `~/项目/codexprojects/U-L-S 综合验收/HANDOFF.md` 追加一条，写清四件事：做了什么、没做什么、下一步、有没有未合并的依赖分支。append-only，不改别人写过的段落。

## 本机执行注意

- WorkBuddy 侧跑 `make dev-setup`、`make migrate` 这类 python 驱动脚本时，若 `python3` 命中注入的 `sitecustomize` 代理并报 `FileExistsError ... mkdir`，用 `PYTHONPATH=` 前缀清掉注入后重跑。
- 犇马侧需要 `HOME=/Users/neomei`，否则找不到 Docker Compose 插件（沙箱 `HOME` 指向别处）。属于环境差异。
