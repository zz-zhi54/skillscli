# AGENTS.md

## 项目概览与代码导航

skillscli 是用 Git 仓库管理 Agent Skills 的轻量 Go CLI。只提供 `add`、`sync`、`remove`、`list` 四个业务命令；没有数据库、开发服务器、永久 Git 缓存、插件或后台服务。

- Go 最低版本为 **1.22.0**，模块名与依赖以 [go.mod](<go.mod>) 为准。
- 主要依赖：Cobra（命令行）、`gopkg.in/yaml.v3`（配置与 Frontmatter）、`golang.org/x/mod/sumdb/dirhash`（内容指纹）；Git 通过系统进程调用。
- [README.md](<README.md>) 描述用户可见行为；[实施计划](<docs/implementation-plan.md>) 记录设计、验收和历史阶段。行为判断以当前源码为准，不要将历史阶段说明当成当前缺失功能。

| 入口 / 包 | 职责 |
| --- | --- |
| [main.go](<main.go>) | 进程入口、信号取消、最终错误输出和退出码。 |
| `internal/cli` | Cobra 参数、作用域选择及安全输出；入口为 [root.go](<internal/cli/root.go>)，业务规则转交同步包。 |
| `internal/config` | 配置模型、严格 YAML 校验、作用域路径、保留注释与样式、原子写回；参见 [model.go](<internal/config/model.go>)、[document.go](<internal/config/document.go>)。 |
| `internal/git` | 引用解析、系统 Git 调用和临时检出；参见 [ref.go](<internal/git/ref.go>)、[fetch.go](<internal/git/fetch.go>)、[command.go](<internal/git/command.go>)。 |
| `internal/skills` | 只读发现、Frontmatter 校验、目录安全检查和 `h1:` 指纹；参见 [skills.go](<internal/skills/skills.go>)、[validate.go](<internal/skills/validate.go>)。 |
| `internal/sync` | [planner.go](<internal/sync/planner.go>) 生成来源级计划；[execute.go](<internal/sync/execute.go>)、[manage.go](<internal/sync/manage.go>) 共用部署事务；[deploy.go](<internal/sync/deploy.go>) 与 [recover.go](<internal/sync/recover.go>) 负责回滚和恢复。 |

## 环境准备与开发

需要 Go 及 PATH 中可执行的系统 Git。以下命令均从仓库根目录运行：

```sh
go version
git --version
go mod download
go run . --help
```

无需数据库、环境变量文件或服务初始化。没有配置的热重载流程，修改后重新执行 `go run` 或构建即可。

```sh
# 构建本机可执行文件；输出目录已被忽略，不覆盖根目录已有程序
go build -o dist/skillscli .
./dist/skillscli --help
# Windows 本机构建
go build -o dist/skillscli.exe .
```

手工验证写命令时使用隔离的临时项目和本地 Git fixture，不在本仓库或真实用户全局作用域执行 `add`、`sync`、`remove`。项目作用域严格取当前工作目录，不向上查找 Git 根目录；`-g/--global` 使用用户主目录，两者互不合并。运行期布局为 `.agents/skillscli.yaml`、`.agents/skills/<name>` 和 `.agents/.skillscli.lock`，不是开发配置。

## 验证与测试

提交代码变更前，按影响范围运行以下检查，并如实报告失败、跳过或未验证部分：

```sh
# 应无输出；入口已移到根目录，不使用历史 cmd 路径
gofmt -l main.go internal
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build ./...
# 仅编译检查，不代表目标平台运行测试通过
GOOS=windows GOARCH=amd64 go build ./...
GOOS=linux GOARCH=amd64 go build ./...
```

使用 `gofmt -w <changed-files>` 格式化改动的 Go 文件，避免无关的全仓库格式化。当前没有额外配置的 lint 工具或覆盖率门槛；如存在 `.github/workflows`，以其中实际检查为准。

测试使用标准库 `testing`，位于对应包的 `*_test.go` 中；集成测试也在普通 `go test` 中运行，不需要额外服务或构建标签。例如：

```sh
# 聚焦同步业务包
go test -count=1 ./internal/sync
# 四个命令的完整生命周期回归
go test -count=1 ./internal/cli -run '^TestEndToEndLifecycle$' -v
# 按需查看覆盖率
go test -cover ./...
```

- 使用 `t.TempDir()`、隔离的主目录和本地临时 Git 仓库；测试不依赖真实远程网络或认证。
- 优先验证项目业务：来源级冲突与修改保护、缺失/删除语义、作用域隔离、配置保留、故障回滚、中断恢复及凭证脱敏。
- 复用现有 fixture 和事务故障注入点；不要重复测试 Go、Cobra、YAML 或 dirhash 的已知行为。目录删除 fixture 必须确认目标处于预期临时根目录之内。
- 涉及进程工作目录或环境变量的测试不要随意增加 `t.Parallel()`。权限、符号链接、FIFO 和平台锁测试可能按环境跳过；阅读实际测试输出。
- 已有文档记录的运行验证平台为 macOS；Windows/Linux 的交叉编译不能证明文件占用、权限、rename 或恢复行为正确。平台代码修改应在实际目标系统验证，不能验证时明确说明。

## 编码与职责边界

- 采用项目现有 Go 命名、导入分组和 `gofmt` 风格。导入业务同步包时沿用 `skillsync` 别名，避免与标准库 `sync` 混淆。
- 文档、CLI 帮助和源码说明以中文为主，标识符保持现有英文风格；终端结果和错误沿用附近代码的语言，不做无关翻译。
- 保持小型内部包和显式数据流。优先标准库与现有依赖，不为未来功能预建通用框架或引入服务。
- CLI 不自行复制 Skill 或修改 lock；规则集中在业务包。规划阶段不写安装目录或 YAML，执行阶段只消费 `Ready` 来源。
- 使用 `context.Context` 传递取消；内部包返回错误，不调用 `os.Exit`。入口统一处理最终错误，避免重复打印或 catch-log-rethrow。
- 仅在事务回滚、资源清理或必要上下文补充处处理错误。使用 `errors.Is`、`errors.As` 和 `%w` 保留错误语义；需要保留多个失败时沿用 `errors.Join`。
- `git.Checkout` 与同步 `Plan` 持有临时检出，使用完必须 `Close()`；关闭前完成快照消费，并保留清理失败。不要并发清理仍在使用的快照。

## 必须保持的业务与安全不变量

- 区分期望的 source/ref/paths/exclude、lock 中实际安装记录和当前磁盘状态。lock 是所有权及修改保护依据；即使目录缺失，旧所有权也不能自动移交其他来源。
- 普通同步只要一个已管理 Skill 有本地修改，就跳过整个来源且保留其目录和 lock；无关来源仍可继续，整体存在失败/跳过时不能返回成功。
- `--force` 仅放宽已管理对象的本地修改保护，不接管未管理同名对象，不绕过符号链接、特殊文件、路径/所有权冲突或规划后的外部修改。
- `add` 只获取新来源并事务性安装；`remove` 不访问 Git、不检查同来源其他 Skill 的内容，保留来源时更新 exclude，删除最后一个管理对象时删除整个来源记录。
- `list` 必须离线、只读；不创建空作用域文件或目录，不恢复事务，不修复安装，也不更新 lock。
- 写命令在作用域 OS 锁下先恢复中断事务，再处理业务。来源是事务边界，目录替换和 YAML/lock 提交必须整体成功或回滚；保留原始 YAML 字节基线并拒绝过时写回。
- 沿用 `yaml.Node` 合并机制保留注释、来源顺序、字段样式及省略默认值，不改成直接序列化结构体覆盖已有配置。
- Skill 发现只扫描配置路径自身或直接子目录，不递归；不改写上游或安装后的 `SKILL.md`。指纹包含普通文件的相对路径和原始字节，不包含权限、空目录、ACL 或扩展属性。
- Git 使用 `exec.CommandContext` 和独立参数，不拼接 shell；保持禁用 hooks、模板复制、fsmonitor 和子模块递归，不执行来源仓库脚本。
- 配置及 Git URL 可能含凭证；不擅自读取真实运行配置，不输出完整来源、原始 Git stderr、命令参数或 YAML 内容。保持现有诊断和来源标签的安全输出边界。
- 不删除不明临时 Git 目录或 `.skillscli-txn-*` / `.skillscli-cleanup-*`，不覆盖未知对象或删除唯一备份。恢复不能安全完成时保留路径和诊断，先备份、核查外部修改或占用，不靠反复 `--force` 绕过。

## 构建、发布与变更交付

这是单模块 CLI，部署产物为可执行文件。涉及发布时先检查 [VERSION](<VERSION>)、[版本检查脚本](<.github/scripts/version.go>) 及 `.github/workflows` 中的实际流程，不假定本地构建等于发布。版本检查脚本会访问远程 Git 标签和 GitHub Releases，并需要 `gh`，不属于离线单元测试。只有用户明确要求时才执行发布或远程 Git 操作。

用户安装使用 `go install github.com/zz-zhi54/skillscli@<version>`，其中版本来自实际发布标签；预发布不会被 `@latest` 选中，不在文档中硬编码当前版本。

保留已有未提交改动和用户运行期数据。变更说明应交代影响的业务规则、执行的检查和平台验证范围；改变用户行为时同步更新 [README.md](<README.md>)。提交历史同时有中文描述和 `docs:` 风格，没有已配置的统一 PR 标题规则，沿用相关近期提交即可；未经要求不提交、推送或创建/合并 PR。
