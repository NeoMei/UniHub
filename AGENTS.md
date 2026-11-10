# AGENTS.md — UniHub

给在本仓库工作的 AI agent。产品语义见 `CONTEXT.md`，本地环境见 `docs/local-development.md`。

本文件只管一件事：**多个 agent 同时在这个仓库上开发时，怎么不互相踩。**

## 前提

UniHub 可能同时被 Codex 和 WorkBuddy 在各自的 worktree 上开发。两边共享的只有一份 `.git` 历史，工作区、分支、本地栈、依赖都各自独立。

## 工作区

- 用独立 git worktree，不要直接改主工作区 `~/项目/codexprojects/unihub`。
- worktree 根目录读 `~/.codex/config.toml` 的 `git-worktree-root`（当前是 `/Volumes/codex/new-worktrees`）。外置卷没挂载就停下来说明，不要静默回退到内置盘。
- 目录命名 `<用途>/unihub`，用途带所有者前缀，例如 `wb-unihub-pilot/unihub`。
- 任务结束后用 `git worktree remove` 清掉，不要留孤儿目录。

## 分支

- 分支前缀就是所有权：Codex 用 `codex/*`，WorkBuddy 用 `wb/*`。只在自己的前缀下提交。
- `main` 是唯一交接点。任何 agent 不直接推 `main`，合并由人决定。
- 从 `main` 拉新分支。需要别人未合并的改动时，在交接说明里写明依赖的是哪个分支。

## 本地栈

每个 worktree 一个实例名，和分支所有者一致：

```sh
UNIHUB_DEV_INSTANCE=wb UNIHUB_DEV_PORT_OFFSET=200 make dev-setup
```

默认偏移是 100，**从第二个实例起必须显式指定 `UNIHUB_DEV_PORT_OFFSET`**，否则端口和容器名会撞。当前占用：

| 实例 | 偏移 | PostgreSQL | Formance | Casdoor | UniHub API |
|---|---|---|---|---|---|
| （默认） | 0 | 55438 | 53068 | 58000 | 8088 |
| bb | 100 | 55538 | 53168 | 58100 | 8188 |
| wb | 200 | 55638 | 53268 | 58200 | 8288 |

- 新增实例往后顺延 300、400，并更新这张表。
- 不要对同一个实例名并发跑 `make dev-setup`。机制细节见 `docs/local-development.md`。

## 依赖与环境

- `node_modules`、`bin`、`.local` 不跨 worktree 共享，每个 worktree 自己准备。
- `.local/dev.env` 含本实例的密钥和端口，只属于该 worktree：不要复制到别的 worktree，不要提交。
- 不要读或改别人 worktree 下的 `.local`。

## 交接

- 收工前把改动提交到自己的分支并 push。没提交的改动对下一位不可见。
- 接手前 `git fetch`，确认 `main` 与 `origin/main` 一致，读最近的提交说明。
- 交接说明写清四件事：做了什么、没做什么、下一步、有没有未合并的依赖分支。

## 本机执行注意

在本机 macOS 上跑 `make dev-setup`、`make migrate` 这类 python 驱动脚本时，如果 `python3` 命中 WorkBuddy 注入的 `sitecustomize` 代理并报 `FileExistsError ... mkdir`，用 `PYTHONPATH=` 前缀清掉注入后重跑。
