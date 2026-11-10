# AGENTS.md — UniHub

给在本仓库工作的 AI agent。产品语义见 `CONTEXT.md`，本地环境见 `docs/local-development.md`。

本文件只管一件事：**多个客户端同时在这个仓库上开发时，怎么不互相踩。**

## 前提

UniHub 可能同时被 **Codex、犇马（SuperWagie）、WorkBuddy** 三路在各自的 worktree 上开发。三方共享的只有一份 `.git` 历史；工作区、分支、本地栈、依赖各自独立。

三方交接点是**文件**，不依赖任一客户端的 API：

- 代码交接：各仓库 `main` 分支
- 状态记录：`~/项目/codexprojects/U-L-S 综合验收/OPERATIONS.md`（发布/验收记录，**三方只读**，不并发写）
- 交接日志：同目录 `HANDOFF.md`（**append-only**，三方只追加不改写）

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

- 分支前缀就是所有权，只在自己的前缀下提交。`agent/*` 是历史遗留（`agent/parallel-dev-stack` 已合入 main），不再使用。
- **三方都不直接推 `main`**，只走分支 + 由人合并。合并时机：每轮联调开始前，由人在 `main` 上合入已验收的分支；合完各客户端 `fetch` 后从 `main` 拉新分支。
- 从 `main` 拉新分支。需要别人未合并的改动时，在 `HANDOFF.md` 写明依赖的是哪个分支。

## 本地栈

每个 worktree 一个实例名，和分支所有者一致：

```sh
UNIHUB_DEV_INSTANCE=wb UNIHUB_DEV_PORT_OFFSET=200 make dev-setup
```

默认偏移是 100，**从第二个实例起必须显式指定 `UNIHUB_DEV_PORT_OFFSET`**，否则端口和容器名会撞。当前分配：

| 客户端 | 实例 | 偏移 | PostgreSQL | Formance | Casdoor | UniHub API |
|---|---|---|---|---|---|---|
| Codex | （默认）`unihub-dev-` | 0 | 55438 | 53068 | 58000 | 8088 |
| WorkBuddy | `unihub-dev-wb` | 200 | 55638 | 53268 | 58200 | 8288 |
| 犇马 | `unihub-dev-sw` | 300 | 55738 | 53368 | 58300 | 8388 |

- 偏移 100（`bb` 实例）已回收，端口段空闲；新增实例往后顺延 400 并更新这张表。
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
