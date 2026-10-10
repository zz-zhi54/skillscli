# skillscli 实施计划

## 目标和边界

用 Go 编写 Agent Skills 管理工具 `skillscli`。仅实现下列命令及规则，不引入数据库、永久 Git 缓存、后台服务或插件系统，不提前设计通用接口框架。

- `skillscli add <git-url>`：添加来源并安装。
- `skillscli sync`：查询上游并自动同步，无需确认；发现本地修改或冲突时跳过整个来源。
- `skillscli sync --force`：可以覆盖已管理 Skill 的本地修改，绝不覆盖未受管理的同名目录。
- `skillscli remove <name>`：删除 Skill；来源仍有其他 Skill 时维护 `exclude`，防止后续同步重新安装；来源已无 Skill 时清理来源记录。
- `skillscli list`：列出已安装的 Skills。
- 所有命令支持 `-g/--global`；默认项目作用域。不存在 `sync --upgrade`，普通 `sync` 自行检查上游更新。

技术选择：Cobra、`gopkg.in/yaml.v3`、系统 Git（`os/exec`）、`golang.org/x/mod/sumdb/dirhash`；其余优先标准库。只在实际用到时添加依赖。

## 作用域与配置

| 作用域 | 配置文件 | 安装目录 |
| --- | --- | --- |
| 项目（默认） | `<project>/.agents/skillscli.yaml` | `<project>/.agents/skills/` |
| 全局（`-g`） | `~/.agents/skillscli.yaml` | `~/.agents/skills/` |

第一阶段将当前工作目录作为项目目录，不向上搜索 Git 根目录；全局作用域使用 `os.UserHomeDir`。路径解析本身不创建文件或目录。

```yaml
version: 1
sources:
  - source: https://github.com/zz-zhi54/idea-skills.git
    paths:
      - skills
    ref: main
    exclude:
      - idea-debug
    lock:
      source: https://github.com/zz-zhi54/idea-skills.git
      revision: "<full-commit-sha>"
      skills:
        idea: "h1:..."
        idea-database: "h1:..."
```

- `source` 必填，保留系统 Git 支持的 URL、SSH/SCP 等来源形式，不擅自限定协议。
- 省略 `paths` 时默认 `["skills"]`；显式空列表视为错误。
- 空或省略的 `ref` 表示跟踪远程默认分支；指定值支持分支、标签和完整 Commit SHA。
- `exclude` 可选，以 Skill 名称排除。
- `lock` 可省略；存在时 `source`、`revision` 和 `skills` 必填（`skills` 可以为空映射）。由程序维护，记录实际安装的来源、完整 revision 和 Skill 指纹。配置中的 `source` 改变时允许与 `lock.source` 不同，以保留旧安装的实际来源。
- 缺失文件、空文件、只有注释的文件、只有文档标记的文件或空映射视为版本 1 的空配置；非空配置必须声明 `version: 1`。
- 拒绝未知字段、重复 YAML 键、多文档、错误字段类型、非法版本、空来源、空路径和非法锁定数据。完整 revision 接受 Git SHA-1（40 位）或 SHA-256（64 位）十六进制值；指纹为有效 SHA-256 的 `h1:` 编码。
- `yaml.Node` 保留现有注释、字段顺序和标量样式，更新时仅合并实际变化；省略的默认 `paths` 和空 `ref` 不强行补写。YAML 编码器可能规范化缩进、空白，不能保证逐字节保持原格式。
- 写配置之前完成校验和编码，同目录临时文件准备、同步、关闭后原子重命名；失败清理临时文件并保留旧文件；新文件权限 `0600`，已有文件权限保持。拒绝将符号链接当作配置文件写入目标。

## 同步与安全不变量（后续阶段）

- 一个来源可包含多个 Skill；发现范围由 `paths` 指定，各 Skill 含原始 `SKILL.md`，不修改它来记录来源。
- 跨来源同名、同一来源内重名、目标目录已存在但未受管理，都必须检查并报冲突。
- 通过 lock 指纹判断已管理 Skill 的本地变化：整个目录缺失时允许重新安装；目录存在但内容增删改（包括部分文件缺失）时，普通同步保护整个来源，`--force` 仅放宽已管理 Skill 的本地变化保护。未管理同名对象和不安全的本地文件树不能用 force 绕过。
- 来源是事务边界：新增、更新、移除以及 lock 更新整体成功或整体回滚。一个来源冲突不允许只部署其中部分 Skill。
- Git 克隆/下载使用系统临时目录，结束后清理；不使用 shell 拼接命令，复用用户既有 SSH、Git 配置和凭证。
- 安装使用临时准备目录、备份和安全替换；配置提交失败也必须恢复该来源的部署。跨来源冲突需要在任何相关部署前识别。
- 路径验证防止绝对路径、`..` 穿越和符号链接逃逸；不允许写出安装根目录。具体发现、指纹和部署规则在相应阶段落地并测试。

## 阶段 1：Go 骨架、Cobra 与配置（已完成）

实现：

1. 建立 Go module、程序入口（阶段 7 前位于 `cmd/skillscli`，现为根目录 `main.go`）及小型内部包。
2. Cobra 根命令，提供帮助及全局作用域参数；不注册尚未实现的业务命令。
3. 项目级与全局配置/安装路径解析，无文件系统副作用。
4. 配置模型、默认值、读取和校验。
5. 基于 `yaml.Node` 更新配置，尽量保留注释、字段顺序及样式。
6. 配置文件安全写入；此处不包含 Skill 文件部署或替换。

验收：

- `go run . --help`（阶段 1 时为 `go run ./cmd/skillscli --help`）可执行，显示 `-g/--global`。
- 单元测试覆盖空配置、默认值、非法配置、锁数据、注释与顺序保留、不同作用域及安全写入失败保留旧文件。
- `gofmt`、`go test ./...`、`go test -race ./...`、`go vet ./...` 通过。
- 无 Git 下载、Skill 安装、同步算法、空壳业务命令和无关文件改动。

## 阶段 2：Git 引用解析与下载（已完成）

实现：系统 Git 命令调用、远程默认分支查询、分支/标签/完整 SHA 解析、指定版本下载、完整 revision 获取及临时目录清理。

已落地（仅 `internal/git`，未改动配置模块或注册业务命令）：

- `Resolve(ctx, source, ref)`：用 `git ls-remote --symref` 查询默认分支；普通名称同时查询完整分支与标签引用，只接受完全匹配。同名分支/标签报歧义错误，要求完整引用；附注标签使用剥离结果。完整 SHA 支持 SHA-1/SHA-256，其远程可获取性及提交类型由下载校验。
- `Fetch(ctx, source, ref)`：创建系统临时仓库，添加 origin，优先 `--depth=1` 获取选定引用，使用 `FETCH_HEAD^{commit}` 解析实际版本并 detached HEAD 检出。分支/标签更新时记录实际获取的 SHA，不沿用查询时的旧 SHA。
- 固定提交的直接浅获取失败时，获取远程公开分支与标签的完整历史；已有浅边界时使用 `--unshallow`。最终严格校验目标 SHA 是所请求的提交，不能用其他版本代替。服务器不提供目标提交或目标不可达时返回错误。
- 成功返回 `Checkout.Dir` 与 `Checkout.Revision`，调用方使用完成后必须调用 `Close`（建议 defer）；`Close` 可重复调用，只清理本次拥有的目录。失败自动清理，并保留清理失败的错误。
- 使用 `exec.CommandContext` 和独立参数，保留用户 Git/SSH/凭证配置；本地相对来源先转绝对路径。仓库操作显式指定临时 Git 目录和工作树，移除继承的仓库定位环境变量，不修改当前项目仓库或全局配置；禁用 hooks、模板复制、fsmonitor 和子模块递归，不自动执行仓库脚本。
- 认证安全取舍：错误保留命令阶段、执行错误/退出码和上下文取消原因，不回显来源地址、命令参数或 Git 原始 stderr，避免 URL 改写或凭证助手输出泄漏秘密。

验收：用本地临时 Git 仓库测试默认分支、分支推进、标签（含 annotated tag）、固定 commit、非法 ref 和 Git 失败；确认指定 revision 的内容正确，成功/失败均清理下载目录，不依赖真实网络或凭证。

验证结果：

- 本地普通仓库与 bare remote 覆盖非 main 默认分支、普通/完整分支和标签、名称不猜测、同名歧义、轻量/附注标签、历史 SHA、SHA-256 仓库、非法/缺失引用、缺失默认分支与非提交对象。
- 测试二进制作为 Git 替身，仅模拟直接 SHA 获取被拒、失败后已有浅边界、引用在查询与下载之间变化、执行失败和取消；断言查询/fetch 参数、回退边界、实际提交、错误认证安全以及临时目录生命周期。
- 验证成功和失败均不改变当前项目的 HEAD、引用、索引、配置与用户文件；保留用户 URL 改写配置且不执行 hooks 或仓库脚本。
- `gofmt`、`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`go build ./...` 通过。未验证真实托管平台网络或凭证交互，按本阶段要求使用离线本地仓库。
- 该阶段未实现永久 Git 缓存、部分克隆、稀疏检出、Skill 发现或后续同步功能；阶段 3–6 另行实施。

## 阶段 3：Skill 发现与指纹（已完成）

实现：在配置 paths 内发现 Skill、验证仓库内路径和 Skill 名称、处理 exclude，使用 dirhash 计算稳定目录指纹，不改动 SKILL.md。

已落地（新增 `internal/skills`，复用 `config.Source` 与 `git.Checkout.Dir`；不改动前两阶段逻辑）：

- `Discover(checkoutDir, source)`：省略 paths 时使用已有默认 `skills`；路径本身包含 SKILL.md 时作为单个 Skill，否则只扫描直接子目录。支持多路径与 exclude，重叠路径去重，不同目录同名报错；按名称排序返回 `Name`、仓库内 `/` 相对 `Path`、本地绝对 `Dir` 和 `Hash`。候选先验证和检查重名，再应用 exclude；出错不返回部分结果。
- `Validate(dir)`：要求普通文件 SKILL.md 和有效 YAML Frontmatter；检查 name、description 的字符串类型、非空与规范长度，名称支持 Unicode 小写字母/数字及连字符，不允许首尾/连续连字符，且必须与目录名一致。允许可选和扩展字段，不要求正文标题或长度，支持 BOM、CRLF、多行描述；错误指出 Skill 路径和问题，不改写内容。依据 [官方规范](https://agentskills.io/specification) 和官方参考校验器的 Unicode 命名规则。
- 路径以 Git 的 `/` 分隔，使用 `filepath.FromSlash` 转为本机路径；拒绝绝对路径、驱动器/UNC/反斜杠表示、原始 `..` 组件和 Git 元数据路径。逐组件 Lstat 并检查检出边界，不跟随仓库内目录链接；计算指纹或读取候选内容前检查完整 Skill 文件树，拒绝所有符号链接及特殊文件。
- 普通空目录（包括未检出的子模块）不作为 Skill；SKILL.md 若为目录（包括 Gitlink）、符号链接或特殊文件，明确报错。不递归获取子模块，也不执行仓库脚本或命令。
- `Hash(dir)`：安全检查后调用 `dirhash.HashDir(dir, "", dirhash.Hash1)`，包含所有普通文件的相对路径和原始内容，返回 h1 指纹；不加入目录的绝对路径，不受修改时间和文件创建顺序影响。不自行实现文件树摘要算法。

验收：多 Skill、多 paths、默认路径、exclude、空结果、重复名、非法路径、符号链接逃逸均有测试；相同内容指纹一致，文件增删改会改变指纹。

已补充测试：临时目录覆盖发现/验证/指纹集成，包含路径重叠和冲突、非法 YAML/字段/名称、Unicode 长度边界、名称不匹配、资源文件增删改/改名、修改时间、隐藏及二进制文件、空目录、缺失路径、符号链接和 FIFO（macOS/Linux）。离线本地 Git fixture 复用配置读取与 Fetch，验证检出不被修改、安装目录不被创建、配置不被更新、脚本不被执行，以及未检出子模块的文件类型处理。

验证结果：`gofmt`、`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`go build ./...` 均通过；额外通过 `GOOS=windows GOARCH=amd64 go build ./...` 和 `GOOS=linux GOARCH=amd64 go build ./...` 的跨平台编译检查。

边界：与 dirhash.Hash1 一致，不记录执行权限和空目录。调用方提供可信的根目录，系统父路径（如 macOS 的 /var）会规范化；检出目录在验证/指纹计算期间必须保持不变，不支持并发清理或恶意替换文件。未验证 Windows/Linux 的实际文件系统运行行为，不使用远程网络作为测试依赖。

该阶段未实现同步计划、本地冲突判断、安装/删除、事务回滚、YAML lock 更新或 add/sync/remove/list 业务命令。

## 阶段 4：同步计划与冲突（已完成）

实现：新增 `internal/sync`，复用配置、Git 获取及 Skill 指纹模块，比较当前 source/ref 的完整上游快照、lock 和本地状态，仅生成来源级计划，不部署文件、不更新配置、不注册业务命令。

已落地：

- `Load(ctx, paths, Options{Force: ...})` 读取 YAML 及 lock；`Build(ctx, cfg, skillsDir, options)` 支持已有配置模型。输出 `Plan`、`SourcePlan`、`SkillAction` 和结构化 `Conflict`；输入配置深复制，规划不修改安装目录、YAML 或输入模型。
- 本地检查先于 Git 获取，覆盖所有 lock.skills（包括被 exclude、paths 或新来源移除的 Skill）。整个目录缺失为 `missing`，不视为修改；存在的完整文件树指纹与 lock 相同为 `unmodified`，文件增删改及部分缺失为 `modified`。未管理同名对象不读取内容，也不按相同指纹接管。
- 每个来源只获取一个当前 source/ref 的目标 Commit，记录实际下载版本；不根据旧 revision 相同就跳过检查。固定 Commit 不追踪默认分支，新设备不要求先下载或恢复旧 lock.revision。来源变化保留旧 lock 的所有权和安全检查，再按新来源计算目标集合。
- 操作包含 `add`（首次安装、新增、完整缺失重装）、`update`（上游指纹变化、force 恢复管理对象修改）、`delete`（上游删除或配置不再选择）、`keep`。目标集合中不存在且本地也已缺失的对象为 keep，但仍需在后续部署成功时移出 lock。只有 revision/source 等 lock 元数据变化时可以 ready 且全为 keep。
- 来源状态为 `ready`、`unchanged`、`skipped` 或 `failed`。**第五阶段只能执行 ready 来源的候选操作，skipped/failed 的操作仅用于报告，绝不能部分执行。** 普通模式有任一管理对象修改就跳过整个来源；force 允许恢复管理对象，即使 Commit 未变。未管理同名对象、链接/特殊文件及无法安全计算指纹的本地树，force 也不可绕过。
- 跨来源对完整目标集合和旧 lock 所有权的并集检查名称冲突，包括目标删除、完整缺失、跳过或 Git 失败来源的旧所有权；不自动跨来源移交同名 Skill。所有相关来源整体跳过；失败来源保留 failed 与原错误，其他独立来源继续规划。同名来源地址用 YAML 下标区分。
- 来源按 YAML 顺序、操作和目标 Skills 按名称、冲突按名称/类型/来源下标稳定排序；跨来源冲突在全局列表只记录一次。不依赖 map 遍历顺序（临时检出路径本身每次不同）。
- 为处理 Git 不保留空目录、新上游删除最后一个 Skill 的情况，`skills.DiscoverSnapshot` 将缺失的配置发现目录视为空集合，复用原发现逻辑；仅放宽配置目录不存在，非法路径、链接、重名和候选验证/指纹错误仍拒绝。原严格 `Discover` 行为保持不变。修改 paths 指向不存在的目录同样意味着空的选中集合。
- 计划持有临时 Git 检出目录，并提供完整目标路径、指纹和 Commit 供第五阶段直接消费。成功返回后调用方必须 `defer plan.Close()`，在 Close 前完成使用；Close 可重复执行，清理失败保留错误且可重试。全局取消/失败时自动清理已获取资源。来源级错误由 SourcePlan.Err 报告，不中断独立来源；其临时资源也由 Plan.Close 统一清理。

验收与测试：临时目录和离线本地 Git 仓库覆盖首次安装、上游变化、本地修改与 force、未更新 Commit 的恢复、完整缺失与部分缺失、上游新增/删除（含发现目录完全消失）、paths/exclude/ref/source 变化、固定 Commit、新设备仅有 YAML 与不可获取旧 lock、来源级跳过及独立来源继续、跨来源同名与旧所有权保护、未管理同名目录/文件/断链、lock 路径穿越和不安全文件树、决策排序、输入无别名、安装/YAML 只读以及成功/来源失败后的临时资源清理。不重复测试 Git、YAML 或 dirhash 内部实现。

验证结果：`gofmt`、`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`go build ./...` 全部通过。第一次全量测试被工作区沙箱拒绝访问工作区外的 Go 构建缓存，申请扩大权限后原命令重试通过；不是业务测试失败。测试仅使用临时目录及本地 Git，不依赖公共网络。未验证跨平台实际文件系统运行或并发目录替换；无本阶段已知未解决功能问题。

边界：规划时安装根目录及其父路径由调用方提供并可信，安装根本身禁止链接。规划、消费快照期间本地和检出树须保持不变；不支持并发替换或并发 Close。指纹仍不包含执行权限和空目录。业务命令、部署、安全替换、备份回滚及 YAML lock 更新留给第五阶段；本阶段未实现。

## 阶段 5：部署、回滚与 sync（已完成）

实现：来源级准备目录、备份、安全替换与回滚；实现 sync 和 --force；成功后更新 lock 并安全写入配置；Git 和部署临时资源清理。

改动文件：

- 命令与取消信号：[root.go](../internal/cli/root.go)、[sync.go](../internal/cli/sync.go)、[main.go](../main.go)。
- YAML 原子写回复用及非写入编码：[document.go](../internal/config/document.go)。
- 执行与事务：[execute.go](../internal/sync/execute.go)、[deploy.go](../internal/sync/deploy.go)、[recover.go](../internal/sync/recover.go)。
- 作用域锁：[lock.go](../internal/sync/lock.go)、[lock_unix.go](../internal/sync/lock_unix.go)、[lock_windows.go](../internal/sync/lock_windows.go)。
- 离线集成/回归测试：[sync_test.go](../internal/cli/sync_test.go)、[deploy_test.go](../internal/sync/deploy_test.go)、[recover_test.go](../internal/sync/recover_test.go)。
- 实施记录：[implementation-plan.md](implementation-plan.md)。

已落地：

- 复用阶段 4 的 `Build`、`Plan`、`SourcePlan`、`SkillAction`，只消费 ready 来源，不重新生成同步决策。`Run` 在整个恢复、规划、部署和 `Plan.Close()` 期间持有作用域锁；项目与全局锁相互独立。Unix 使用 `flock`，Windows 使用 `LockFileEx`，不依赖 PID 文件或陈旧锁抢占。空 `.skillscli.lock` 文件有意保留，操作系统在进程退出时释放锁。
- Cobra 仅新增 `sync`，支持 `sync`、`sync --force`、`sync -g`、`sync -g --force`（也支持 `--global`）。拒绝位置参数和 `--upgrade`，没有用户确认。按 YAML 来源顺序输出成功、No changes、Skipped、Failed 和 Added/Updated/Removed；冲突或失败返回非零退出码。普通错误不回显 URL 凭证、Git 输出或 YAML 内容；人工恢复错误保留具体本地事务路径和原因。
- 正常同步保护本地修改并跳过整个来源。force 只恢复已管理对象，不接管未管理同名目录/文件，不绕过链接、特殊文件或跨来源所有权冲突。部署重新验证来源及 lock 所有权、目标路径、全部本地指纹/存在状态、目标快照，以及原始 YAML 字节；force 也不能绕过计划生成后的用户修改。名称拒绝跨平台保留设备名及大小写别名冲突。
- 在同一 `.agents` 文件系统创建 `.skillscli-txn-*`，先完整复制并验证所有新增/更新 Skill，再同步写入不可变 journal、原始 before YAML 和预计 after YAML。整个 Skill 目录替换，原样复制 SKILL.md 和资源文件，保留普通文件执行位，不执行仓库脚本。所有待更新/删除目录先重命名备份，然后逐个安装；目标必须不存在，不依赖覆盖非空目录的 rename 行为。
- 所有文件安装及指纹检查完成后才用已有 YAML 节点合并和原子写回更新该来源 lock，记录实际目标 Commit、source 和完整目标 skills 指纹映射。未变化来源不改目录、文件时间、inode 或 YAML；仅 lock 元数据变化也可独立提交。成功来源保留结果，其他普通来源失败继续处理，不回滚先前成功来源。
- 提交标记先写临时文件、Sync/Close 成功，再原子发布。提交之前的错误逆序通过 rename 恢复旧目录及原始 YAML 字节；恢复成功后重新读取 Document，避免影响后续来源的注释或版本。只有真实提交的 YAML 字节才推进下一来源基线。结束时先将事务原子改名为 `.skillscli-cleanup-*` 终态目录，再可重入清理；删除中断不会被误认成待回滚事务。只在终态垃圾上放宽只读权限以便删除，不修改活跃备份权限。
- 下次 sync 在规划前恢复中断事务。通过 new/old/正式目录位置和指纹判定进度，可继续一次未完成的回滚；完整提交仅清理，不恢复旧版。准备阶段无 journal 且无原备份可直接清理，缺失/损坏 journal 但存在备份时拒绝删除。正常结束会关闭 Plan 并清理 Git 检出；SIGINT/SIGTERM 通过 context 取消，让回滚和清理完成。

验收与离线测试：临时项目、临时 HOME 和本地 Git 覆盖首次安装、重复同步、上游新增/修改/删除、exclude 移除、整目录缺失重装、元数据更新、本地增删改和 force、未管理目录/文件、符号链接、过期计划/所有权/YAML 拒绝、多 Skill 来源事务、多个来源前后失败隔离、原始 YAML 注释和省略默认值。故障覆盖准备、备份、安装、YAML 校验/真实写入、保存后、提交标记写入以及取消；子进程在准备/备份/安装/保存后终止，验证 OS 锁释放和下次恢复。另覆盖部分回滚重入、提交清理、残缺终态清理、只读备份清理、用户在中断后修改正式内容或 YAML、缺失日志保留唯一备份、force 同 revision 失败后独立来源成功、恢复后 Document 注释和版本，以及成功/失败后的 Git 临时目录清理。CLI 覆盖四种命令组合、输出顺序、非零错误和凭证脱敏；无需公网。

最终验证结果：`gofmt`（并确认 `gofmt -l cmd internal` 无输出）、`go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...`、`go build ./...` 均通过；`GOOS=windows GOARCH=amd64 go build ./...` 和 `GOOS=linux GOARCH=amd64 go build ./...` 跨编译均通过。最终修改后再次完整运行上述检查，退出码为 0。真实 YAML 写入失败、提交标记写入失败和终态清理失败隔离测试另外以 verbose 模式确认 PASS，非跳过。此前 CLI/全量测试曾被沙箱阻止访问工作区外的 Go 缓存，批准后原命令重试通过；不是功能测试失败。安全只读复核确认提交字节基线、标记原子发布和终态清理的三个具体边界问题均已修复。

人工恢复与边界：

- 用户在中断后修改已安装新版或 YAML、日志/快照损坏、备份缺失、权限/文件占用阻止恢复时，自动恢复停止，返回保留目录和原因；绝不覆盖未知对象或删除唯一原备份。先备份该目录和用户修改，核对 journal、`config.before`、`config.after`、old/new 及正式目录，手工恢复一致状态（或撤销外部修改后重跑 sync）。不要直接删除仍含唯一备份的 `.skillscli-txn-*`。无法安全恢复的共享 YAML 基线会阻止当前作用域继续部署；这是安全例外，普通来源失败仍继续处理其他独立来源。终态 `.skillscli-cleanup-*` 不再参与回滚，可以在权限/文件占用问题解除后重试清理。
- 保证进程终止后的恢复，不承诺突然断电、磁盘损坏下的持久原子性；文件会 Sync，但没有对父目录 fsync。多目录更新只对 cooperating skillscli 进程串行化，不对其他读写程序提供瞬时可见的整来源原子视图。便携标准库路径重验可缩小竞态窗口，不能完全排除恶意外部程序在最后一刻替换目录的命名空间竞态。
- 指纹仍与 dirhash 一致，不包含文件权限或空目录；文件权限复制不等于 ACL/扩展属性克隆。强制终止（如 SIGKILL）无法执行 `Plan.Close()`，系统临时目录可能留下不参与安装恢复的 Git 检出；不扫描或删除无法安全证明属于本进程的系统临时目录。没有永久缓存或自动后台回收服务。
- 实际运行测试在 macOS；Windows/Linux 的跨编译不能替代真实文件系统测试。Windows 文件占用可导致 rename 或恢复失败，保留备份并要求解除占用后重试；Unix 权限故障测试在 Windows/高权限进程下按 fixture 条件跳过。

## 阶段 6：add、remove、list 与文档（已完成）

已落地：

- [命令注册](<../internal/cli/root.go>)与[新命令](<../internal/cli/manage.go>)：严格参数检查、全局 flag、安全来源标签/诊断；无交互、不提供 add --ref 或 sync --upgrade。
- [管理 API](<../internal/sync/manage.go>)：add 只规划新来源并与已有 lock 做所有权冲突检查；成功部署后才持久化 source+lock。重复 source 字符串、空发现集合及未管理同名目标拒绝。remove 按 lock 定位，只检查/删除指定 Skill，不调用 Git、不同步其他来源/兄弟 Skill；保护修改，force 不绕过所有权或路径安全。保留来源时 exclude 去重，最后一个 Skill 删除整个来源。list 只读取 YAML/本地指纹，不获取 Git、不恢复事务、不创建目录/锁文件，来源顺序与名字排序稳定。
- [作用域打开](<../internal/sync/execute.go>)复用规范路径、OS 锁、恢复、配置基线检查；[共用部署器](<../internal/sync/deploy.go>)接收既有 SkillAction 与目标配置，保持准备、目录重验、备份、安装、YAML 保存、提交、回滚/清理链路。sync 保留原来源级行为；[配置克隆](<../internal/sync/planner.go>)避免目标模型共享可变 lock。
- [YAML 保真](<../internal/config/document.go>)按索引同时移除原节点与模型来源，避免同 URL 不同选择来源删除后错配注释；[默认值匹配](<../internal/config/yaml.go>)逐个消费同 URL 旧项。修复审查发现的显式默认路径/注释丢失问题。
- [恢复](<../internal/sync/recover.go>)修复 Update/Delete 的原备份和正式目录都缺失时误认成功的问题；停止并保留事务，不写回虚假的旧 lock。
- [中文 README](<../README.md>)说明构建、四个命令、作用域、配置/ref/lock、修改保护、失败恢复及平台边界。未新增依赖、缓存、数据库、后台服务或插件，未修改 SKILL.md。

本地 Git 验收结果（通过实际 Cobra 命令树；事务故障使用真实 fixture 与共用部署器注入项目自有阶段）：

| 场景 | 结果/覆盖 |
| --- | --- |
| 1. 首次 add | 默认 skills、默认分支、目录/源/实际 revision/指纹正确；SKILL.md 字节不改写 |
| 2. 多 Skill | 同来源全量安装，稳定 list 顺序 |
| 3. 重复/自定义 --path | 多路径、单 Skill 路径、扫描目录、保存 paths、未选对象不安装 |
| 4. 重复 add | 同作用域拒绝并保持 YAML 原字节 |
| 5. 上游更新 sync | 内容及实际 lock revision 更新 |
| 6. 本地修改保护/force | 整来源普通跳过，force 恢复；不覆盖未管理对象 |
| 7. 上游新增/删除 | 新增安装、删除移除并更新 lock |
| 8. remove 后 exclude | 只删指定 Skill，exclude 去重；sync 不重装，缺失对象也可移记录 |
| 9. 强制 remove | 普通拒绝且状态不变，force 移除已管理修改目录 |
| 10. 最后 Skill | 删除整个来源；空 list/sync 成功；同 URL 来源保留正确节点/注释 |
| 11. list 状态 | ok/modified/missing，禁用 Git 仍可运行；文件/YAML 不变，空作用域不创建目录 |
| 12. 作用域隔离 | 同 URL 可分别 add；全局 list/remove 不影响项目 |
| 13. 跨来源冲突 | 缺失目标仍由旧 lock 占有；冲突 add 不留新来源或部分目录 |
| 14. add 失败回滚 | 首次/已有配置的 prepare/install/save/saved 故障恢复，兄弟修改不受影响 |
| 15. remove 失败回滚 | 有兄弟/最后 Skill 的 backup/save/saved 故障恢复原 YAML 和本地修改目录 |
| 16. 幂等性 | 重复 sync 无变化且 YAML 字节不变；重复 add/remove 安全拒绝 |
| 17. 分支/标签/SHA | 配置分支跟随推进、附注标签选择正确、完整 SHA 固定不随默认分支推进 |

测试位置：[CLI 验收](<../internal/cli/manage_test.go>)、[既有 sync 回归](<../internal/cli/sync_test.go>)、[管理事务与边界](<../internal/sync/manage_test.go>)、[YAML 回归](<../internal/config/document_test.go>)、[恢复回归](<../internal/sync/recover_test.go>)。既有 Git、发现、规划、部署与中断恢复测试同时保留。

集中审查：检查配置/YAML、Git 获取/清理、发现/校验/指纹、计划、事务/恢复、作用域锁和 CLI 边界。两项确认缺陷已修复并有回归；未发现 force 接管未管理对象的问题。大小写别名仍在部署前所有权校验拒绝，不新增另一套同步/复制逻辑。

验证结果（均已实际执行通过）：

- `gofmt` 格式化修改的 Go 文件；`test -z "$(gofmt -l cmd internal)"` 确认项目源码无未格式化文件。
- `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...`、`go build ./...`。
- `GOOS=windows GOARCH=amd64 go build ./...`、`GOOS=linux GOARCH=amd64 go build ./...`；额外通过 Windows/Linux arm64 同样命令的交叉编译。
- `go run . --help`（阶段 6 时为 `go run ./cmd/skillscli --help`）确认进程入口展示 add/sync/remove/list 与作用域参数，无 completion 或 upgrade 业务命令。
- 初次测试尝试因工作区外 Go 构建缓存写权限被 sandbox 阻止，按相同命令授权重试后通过；这不是功能测试失败。最终 race/vet/build/四组跨编译/入口 smoke 的串联命令退出码为 0。

未发现尚待修复的已知功能问题；两个审查确认问题均已修复。没有执行发布、打标签或项目 Git 提交。

边界与日常使用判断：继承阶段 5 的持久性、外部并发视图/竞态、权限/空目录指纹及强制终止临时 Git 遗留边界。实际运行验证仅 macOS，跨编译不等于 Windows/Linux 文件系统运行验收。可在已验证环境与上述边界内日常使用；其他平台和真实远程认证仍需实际环境验证，不声称已经全平台运行验收。

## 阶段 7：入口调整与首次开源测试版发布（v0.1.0-beta.2）

在用户明确授权后执行，不新增业务功能。

发布范围调整：首次发布时曾误按正式版创建 `v0.1.0` 标签与 Release，经用户确认已删除远程标签与 Release，改为预发布 `v0.1.0-beta.1`。但 Go 模块代理已缓存 `v0.1.0`，导致 `@latest` 仍解析到它。因此发布 `v0.1.0-beta.2`，并在 [go.mod](<../go.mod>) 中以 `retract v0.1.0` 撤回该版本；README 指向 `v0.1.0-beta.2`。`v0.1.0` 仍可被显式安装，但 Go 会给出撤回提示。

已落地：

- 程序入口由 `cmd/skillscli/main.go` 移到项目根目录 [main.go](<../main.go>)，仍为 `package main`，继续调用 `internal/cli` 中的 Cobra 命令树；旧 `cmd/` 目录已删除。模块路径保持 `github.com/zz-zhi54/skillscli`，`go install github.com/zz-zhi54/skillscli@latest` 因此直接解析到根包，`go build -o skillscli .` 与 `go run . --help` 同样可用。
- 许可证由 MIT 改为 Apache License 2.0：[LICENSE](<../LICENSE>) 正文为 apache.org 官方全文，附录版权占位符填为 `Copyright 2025 zz-zhi54`（沿用原 LICENSE 的版权持有人与年份）。直接依赖均为与 Apache-2.0 兼容的宽松许可；项目不分发依赖源码，因此未添加 NOTICE。
- README 更新为根目录入口的安装与构建命令，许可证统一为 Apache-2.0 并说明主要依赖的许可证。
- 业务代码、Git 获取、同步规划与事务部署逻辑未改动；add/remove/list/sync、`--force` 与 `-g` 行为不变。

验证（macOS，实际执行）：`gofmt -l .` 无输出；`go build -o skillscli .`；`go test -count=1 ./...`；`go test -race -count=1 ./...`；`go vet ./...`；`go mod verify` 输出 all modules verified；`go mod tidy -diff` 无差异；`go install .`（GOBIN 指向临时目录）成功；临时项目与本地 Git 仓库冒烟运行 add/list/sync/remove 正常。

边界：只在 macOS 上运行验证；Windows/Linux 仅交叉编译检查，不代表真实文件系统与文件占用行为已通过运行测试。

## 当前执行约束

阶段 1–6 完成时要求不发布、不打标签、不提交 Git；阶段 7 已获得用户明确授权执行首次发布。复用现有配置模型、Git 获取、发现/指纹、同步计划及安装/恢复事务；不引入另一套复制/同步决策，不改写 SKILL.md。不实现永久缓存、Store/数据库、后台服务、插件、`sync --upgrade`、额外业务命令或交互确认，不新增外部依赖。Windows/Linux 仅交叉编译，不将其表述为实际运行验收。
