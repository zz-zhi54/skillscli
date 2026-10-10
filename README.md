# skillscli

用 Git 仓库管理 Agent Skills 的轻量命令行工具。只有 `add`、`sync`、`remove`、`list` 四个业务命令；配置和安装内容保存在当前项目或用户主目录，不需要数据库、后台服务、插件或永久缓存。

## 为什么创建 skillscli？

我自己维护了一些 Skills，也会使用多个开源项目提供的 Skills。以前通过 [vercel-labs/skills](https://github.com/vercel-labs/skills) 等工具安装单个 Skill 很方便，但用得越来越多以后，问题变成了：**自己的 Skills 和第三方 Skills 分散在不同仓库，缺少统一管理的办法**。想知道装了哪些、来自哪里，或者统一更新，都不够方便。

另一个让我困扰的是，**上游删除了某个 Skill，本地旧目录却可能还留着**。时间一长，就容易积累已经不再维护的内容。我希望同步不只是下载新文件，还能正确反映上游的新增、修改和删除，同时保护自己改过的内容。

再加上换电脑或重装环境时，尤其是全局 Skills，需要重新寻找来源、逐个安装，维护起来很麻烦。这些实际使用中的问题，是我创建 skillscli 的原因。

## 安装与前提

需要 Go **1.22 或更新版本**及 PATH 中可执行的系统 **Git**。

推荐使用 `go install` 安装最新版本，或固定到发布版本 `v0.1.0`：

```sh
go install github.com/zz-zhi54/skillscli@latest
go install github.com/zz-zhi54/skillscli@v0.1.0
```

也可以在本项目根目录构建（程序入口位于根目录 `main.go`）：

```sh
go build -o skillscli .
./skillscli --help
# 或不生成二进制直接运行
go run . --help
```

Windows 可使用 `go build -o skillscli.exe .`。把生成的程序放到 PATH 后即可直接运行 `skillscli`。当前没有自动升级或 `sync --upgrade`；升级请重新执行 `go install` 并指定新版本。

## 快速开始

以下 URL 只是示例，请替换成你信任的实际仓库；也可以使用本地 Git 仓库的绝对路径进行离线操作。

```sh
# 在当前项目安装；默认发现仓库内的 skills 目录
skillscli add https://example.com/team/agent-skills.git
skillscli list

# 从来源当前 ref 获取最新快照（默认 ref 是远程默认分支）
skillscli sync

# 删除一个已管理的 Skill；后续 sync 不会重新安装它
skillscli remove code-review

# 全局作用域：所有命令都可加 -g / --global
skillscli add https://example.com/team/personal-skills.git -g
skillscli list -g
skillscli sync -g
skillscli remove writing-helper -g
```

### 两个独立作用域

| 作用域 | 配置与 lock | Skill 安装位置 |
| --- | --- | --- |
| 项目（默认） | 当前工作目录的 `.agents/skillscli.yaml` | 当前工作目录的 `.agents/skills/<name>` |
| 全局（`-g`） | 用户主目录的 `.agents/skillscli.yaml` | 用户主目录的 `.agents/skills/<name>` |

不向上搜索项目根目录，不把两个配置合并，也不会把全局配置复制到项目。需要在预期项目目录运行命令。用户主目录由操作系统环境解析。

## 命令

### `add <git-url> [--path <path>]... [-g]`

首次添加来源并安装发现的所有 Skills；不交互选择，不修改上游或安装后的 SKILL.md。

```sh
skillscli add https://example.com/team/agent-skills.git --path tools --path standalone/code-review
```

`--path` 可重复使用，顺序写入配置；省略时是 `[skills]`。路径是仓库内使用 `/` 分隔的相对路径。路径本身有 SKILL.md 时安装该 Skill，否则只扫描直接子目录，不无限递归；重叠路径去重。绝对路径、`..`、反斜杠和 Git 元数据路径被拒绝。缺失路径在同步中可表示空快照；但 add 一个没有发现任何 Skill 的来源会失败。

添加跟踪远程**默认分支**，不提供 `--ref`。安装与来源/lock 写回同属一个事务，失败不会留下新的来源、lock 或半安装内容。只获取新来源，不顺带同步已有来源。同作用域已经存在的相同 `source` 字符串会拒绝，提示改用 `sync`；不同 URL/本地路径别名不做 Git 地址规范化。存在其他来源占有的同名 Skill，或未管理的同名本地对象时拒绝安装，不自动接管，即使目录内容相同也不接管。

本地仓库建议使用绝对路径：相对 source 会按每次运行时的工作目录解释，全局作用域尤其不要使用相对 source。

### `sync [--force] [-g]`

按配置的每个来源获取目标快照，比较上游、lock 和本地内容，新增、更新或删除 Skills。上游新增会安装，上游删除会删除未修改的已管理目录，缺失的已管理目录会重装；成功后记录实际下载的 Commit 和目标内容指纹。固定完整 SHA 不跟踪分支。

```sh
skillscli sync
skillscli sync --force
```

普通同步发现某来源的已管理 Skill 有本地修改，会**跳过整个来源**，原目录和 lock 保持不变；其他无关来源仍可继续。下载失败、名字冲突等会报失败/跳过并返回非零退出码，不能把部分失败当作全局成功。来源之间不自动移交同名目录，即使原目录已缺失，旧 lock 的所有权仍有效。

`--force` 允许覆盖已管理目录的本地修改，也允许删除不再被选择的已修改 Skill。它不会覆盖未管理对象，也不能绕过符号链接、特殊文件、路径或所有权冲突。执行前请备份有价值的本地修改。

### `remove <name> [--force] [-g]`

通过当前作用域的 lock 定位 Skill，只删除指定安装目录并更新对应来源；**不获取 Git，也不同步/检查同来源其他 Skill 的内容**。

- 普通模式拒绝删除已修改的管理目录；`--force` 可删除这样的目录，但不能删除未管理对象或绕过不安全路径。
- 已管理目录完整缺失时也可移除其记录。
- 来源还有其他 Skill：从 lock.skills 删除该名字，并追加到 exclude（已有项不重复）。这同样适用于单 Skill 发现路径。
- 移除最后一个已管理 Skill：删除整个来源记录，包括 ref、paths、exclude 和 lock。
- 重复 remove 一个已移除的名字会报“未管理”，不会扩大删除范围。

### `list [-g]`

只读、离线列出 lock 中的 Skill。按配置来源顺序，每个来源内按名称排序。显示名字、本地状态及经安全简化的来源标签；标签不是完整 URL，不回显 URL 凭证或查询参数。

```text
code-review    ok          source 1 (agent-skills)
writing-helper modified   source 1 (agent-skills)
missing-helper missing    source 2 (personal-skills)
```

`ok` 表示当前文件树指纹与 lock 相同；`modified` 表示文件新增、删除或内容变化；`missing` 表示整个安装目录缺失。不安全本地树显示 `unsafe`。空作用域输出 `No managed Skills.` 并成功退出，不创建配置、安装根目录或锁文件。

list 不检查远程状态，不恢复中断事务、不修复缺失目录，也不更新 lock。无法读取/验证配置等错误仍会失败。外部程序正修改文件时，不承诺一致的瞬时快照。

## 配置与固定版本

项目和全局都使用以下 YAML 结构；`add` 会生成来源和 lock，也可以手工编辑来源选择后执行 `sync`：

```yaml
version: 1
sources:
  - source: https://example.com/team/agent-skills.git
    ref: main                 # 省略或为空：远程默认分支
    paths: [skills]            # 省略：skills；显式空数组无效
    exclude: [experimental]   # 按 Skill 名称排除
    # lock 由成功安装/同步维护，不要手工修改
```

`ref` 支持分支、标签和完整 Commit SHA；例如 `release`、`refs/heads/release`、`refs/tags/v1`、40 位 SHA-1（也支持 64 位 SHA-256）。短 SHA、revision 表达式和其他 refs 命名空间不受支持。分支/标签同名时必须使用完整引用。标签按每次运行时解析的目标获取，不把标签隐式变成永久固定版本；要严格固定请使用完整 SHA。

自动维护的 `lock` 包含：

- `source`：成功安装时使用的实际来源地址；
- `revision`：实际检出的完整 Commit；
- `skills`：Skill 名字到 `h1:` 内容指纹的映射。

lock 是本地所有权与修改保护依据，不是需要先恢复的旧 Git 快照。不要手动删除 lock 来尝试绕过保护：剩下的安装目录会变成未管理对象并阻止接管。更换 source/ref/paths/exclude 后，sync 仍按旧 lock 检查本地修改。

保存尽量保留已有 YAML 注释、来源顺序、字段风格和省略的默认值；自动管理 lock。不要在命令执行期间手工编辑配置：若原始内容发生变化，事务会拒绝过时写回。配置中可能含来源凭证，请自行控制配置权限、版本控制和备份；CLI 对诊断和来源标签做脱敏不等于配置自动加密。Git 使用用户已有的 SSH、凭证助手和 URL 改写配置。

## Skill 与安全边界

- 每个 Skill 目录需要普通文件 SKILL.md，带有效 YAML Frontmatter 的 `name`、`description`；名字必须与目录名一致，符合小写 Unicode 字母/数字及连字符规则，长度 1–64；description 为非空字符串，最多 1024 字符。
- 不改写 SKILL.md，不执行仓库脚本/hooks，不递归获取子模块；仓库内容仍需由你审查和信任。
- 拒绝 Skill 树内符号链接、特殊文件、路径逃逸以及不便携/保留目录名。安装根本身不能是符号链接；可信系统父路径（例如 macOS 的 /var）会规范化。
- 指纹包括普通文件的相对路径和原始内容，不包括权限、空目录、ACL 或扩展属性。因此只有权限/空目录变化不会显示 modified。

## 事务、失败与恢复

写命令使用同作用域的 OS 文件锁串行化；每个来源有独立事务，临时内容准备好后备份旧目录、安装新版、原子写 YAML 并提交。正常失败尝试恢复原目录（包括本地修改）和原始 YAML 字节。add/remove 复用同一部署与回滚机制。两个作用域的锁彼此独立。

进程中断后，下一次 **add/sync/remove** 在获取作用域锁后先恢复遗留事务，再处理业务。list 不触发恢复。出现备份缺失、安装内容/配置被外部修改、文件占用等不能安全恢复的情况，会停止并保留事务及备份；CLI 提示保留的恢复路径。不要直接删除 `.skillscli-txn-*` 或 `.skillscli-cleanup-*`，尤其不要删除唯一的原目录备份。先备份整个作用域及事务目录、解除占用，并确认是否存在外部修改；无法判断时请人工检查，而不是反复 `--force`。

边界：

- 不承诺突然断电或磁盘损坏下的持久原子性；没有父目录 fsync。
- 多目录操作对其他程序不是瞬时原子视图；不能完全防御恶意外部程序在最后一刻替换路径的文件系统竞态。
- Windows 文件占用可能使 rename、回滚或清理失败；保留备份并解除占用后重试。
- 正常路径关闭临时 Git 检出；SIGKILL 等不可捕获终止可能遗留系统临时 Git 目录，不会盲目扫描删除不明临时目录。

## 开发与验证

```sh
gofmt -l cmd internal
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build ./...
GOOS=windows GOARCH=amd64 go build ./...
GOOS=linux GOARCH=amd64 go build ./...
```

测试使用隔离的本地临时 Git 仓库，不依赖真实远程服务或认证；覆盖完整命令生命周期、两个作用域、引用选择、修改保护、冲突、事务失败与中断恢复。平台特殊测试按实际条件跳过。

当前实际运行验证平台为 macOS；Windows/Linux 交叉编译仅检查可编译性，不代表这些系统上的真实文件系统、权限和文件占用行为已通过运行测试。适合在上述边界内进行日常使用；首次在其他平台使用前建议备份并做实际环境验证。

## 已知限制（v0.1.0）

- 只支持上述四个命令；没有交互式选择、`sync --upgrade`、版本锁定之外的 ref 表达式或插件机制。
- 项目作用域以运行命令时的当前工作目录为准，不向上搜索项目根目录。
- 指纹不含文件权限、空目录、ACL 和扩展属性；这些变化不会触发 modified 保护。
- 跨平台行为仅在 macOS 上经过运行验证，Windows/Linux 只有交叉编译检查。
- 标签不隐式固定为永久版本；需要严格固定请使用完整 Commit SHA。

详细阶段记录见 [实施计划](<docs/implementation-plan.md>)。

## 许可证

本项目基于 [Apache License 2.0](<LICENSE>) 发布，版权信息：Copyright 2025 zz-zhi54。

主要依赖的许可证与 Apache-2.0 兼容：`spf13/cobra` 与间接依赖 `inconshreveable/mousetrap` 为 Apache-2.0；`gopkg.in/yaml.v3` 为 MIT 与 Apache-2.0 双许可；`spf13/pflag`、`golang.org/x/mod` 为 BSD-3-Clause。本仓库不包含这些依赖的源码副本。
