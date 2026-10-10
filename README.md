# skillscli

轻量级 Agent Skills 管理工具。用 Git 仓库统一管理项目或全局的 Skills，支持安装、更新、删除，并保护本地修改。

适合同时维护自己的 Skills 和使用多个第三方仓库的场景：**记住来源，一条命令同步上游的新增、修改和删除**。无需数据库、后台服务或插件。

## 安装

需要系统已安装 **Git**；直接使用编译好的程序，不需要安装 Go。

在 [GitHub Releases](https://github.com/zz-zhi54/skillscli/releases) 下载对应文件：

| 系统 | 文件 |
| --- | --- |
| macOS（Apple 芯片） | `skillscli_darwin_arm64` |
| Windows x64 | `skillscli_windows_amd64.exe` |
| Windows ARM64 | `skillscli_windows_arm64.exe` |
| Linux x64 | `skillscli_linux_amd64` |
| Linux ARM64 | `skillscli_linux_arm64` |

目前不提供 macOS Intel 版本。

**macOS / Linux**：以 Apple 芯片版为例，在下载目录执行：

```sh
mkdir -p "$HOME/.local/bin"
mv skillscli_darwin_arm64 "$HOME/.local/bin/skillscli"
chmod +x "$HOME/.local/bin/skillscli"
```

确保 `$HOME/.local/bin` 已加入 `PATH`（可在 Shell 配置中设置）。Linux 请替换为对应的下载文件名。

**Windows**：将下载的 `.exe` 文件重命名为 `skillscli.exe`，放到固定目录，并将目录添加到用户 `Path`。

安装后运行 `skillscli -v` 验证。升级时从 Releases 下载新文件替换即可，目前没有自动升级功能。

## 快速开始

以下地址只是示例，请替换成实际可信的 Git 仓库：

```sh
# 在当前项目安装仓库 skills/ 下的 Skills
skillscli add https://example.com/team/agent-skills.git

# 查看已管理的 Skills
skillscli list

# 同步上游变化
skillscli sync

# 移除指定 Skill
skillscli remove code-review
```

默认安装到**当前目录的 `.agents/skills/`**。添加 `-g` 或 `--global` 可以改为用户级安装：

```sh
skillscli add https://example.com/team/my-skills.git -g
skillscli sync -g
skillscli list -g
```

## 常用命令

| 命令 | 作用 |
| --- | --- |
| `skillscli add <git-url>` | 添加仓库并安装发现的 Skills |
| `skillscli sync` | 同步所有已配置来源 |
| `skillscli list` | 查看 Skills 及本地状态 |
| `skillscli remove <name>` | 删除指定的已管理 Skill |

常用参数：

- `-g` / `--global`：使用用户级作用域，四个命令都支持。
- `add --path <路径>`：指定仓库中的 Skill 或目录，可重复使用；默认扫描 `skills`。
- `sync --force`：允许覆盖已管理 Skill 的本地修改。
- `remove <name> --force`：允许删除本地已修改的受管理 Skill。
- `-h` / `--help`：查看命令帮助。

`list` 中的 `ok`、`modified`、`missing` 分别表示与本地记录一致、已修改、目录缺失；**`ok` 不代表远程没有更新**。

## 安装位置与配置

| 作用域 | Skills 目录 | 配置文件 |
| --- | --- | --- |
| 当前项目（默认） | `.agents/skills/` | `.agents/skillscli.yaml` |
| 用户级（`-g`） | `~/.agents/skills/` | `~/.agents/skillscli.yaml` |

项目作用域始终是**执行命令时的当前目录**，不会自动寻找 Git 项目根目录。两个作用域相互独立。

`add` 会自动创建配置；需要指定分支、标签或排除某些 Skills 时，可编辑 YAML：

```yaml
version: 1
sources:
  - source: https://example.com/team/agent-skills.git
    ref: main
    paths: [skills]
    exclude: [experimental]
```

`ref`、`paths` 和 `exclude` 均可省略：默认跟踪远程默认分支、扫描 `skills` 目录、不排除任何 Skill。`ref` 也支持标签或完整提交 SHA；严格固定版本时使用完整 SHA。配置中的 `lock` 由工具维护，不要手工修改。编辑来源后运行 `skillscli sync`。

## 同步时需要知道的事

- `sync` 会安装上游新增的 Skills，也会删除上游已经移除、且本地未修改的已管理 Skills。
- 发现本地修改时，默认跳过**整个来源**，避免覆盖用户文件；`--force` 只针对已管理内容，**不会接管或覆盖未管理目录**。
- `remove` 删除指定 Skill；若该来源仍有其他 Skill，会记录排除项，避免下次同步重新安装它。
- 只添加自己信任的 Git 来源。工具不会主动执行仓库脚本，但 Skills 的内容仍需要自行审查。

冲突处理、恢复机制和完整参数说明见 [进阶参考](docs/reference.md)。

## 开发与维护

从源码构建需要 Go 1.22+：

```sh
go test ./...
go build -o skillscli .
```

发布流程及检查规则见 [进阶参考](docs/reference.md#版本与自动发布) 和 [CI 规范](spec/spec-process-cicd-pr-checks-and-release.md)。

本项目采用 [Apache License 2.0](LICENSE)。
