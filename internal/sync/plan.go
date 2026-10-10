// Package sync 规划来源同步，并为 sync/add/remove 提供共用的安装与恢复事务。
package sync

import (
	"errors"

	"github.com/zz-zhi54/skillscli/internal/config"
	gitrepo "github.com/zz-zhi54/skillscli/internal/git"
	"github.com/zz-zhi54/skillscli/internal/skills"
)

// Options 控制已管理 Skill 的本地修改保护，不改变路径安全或名称所有权规则。
type Options struct {
	Force bool // 允许 sync 恢复或 remove 删除已管理的修改；不能接管未管理对象。
}

// SourceStatus 表示整个来源的可执行性，不能由某一条候选操作单独决定。
type SourceStatus string

const (
	// Ready 表示来源可进入部署，包括只需更新 lock 元数据的情况。
	Ready SourceStatus = "ready"
	// Unchanged 表示本地内容和目标 lock 均无需更新。
	Unchanged SourceStatus = "unchanged"
	// Skipped 表示来源有冲突，所有候选操作都只能报告。
	Skipped SourceStatus = "skipped"
	// Failed 表示规划或执行失败，详细原因由 Err 保留。
	Failed SourceStatus = "failed"
)

// ActionKind 表示已管理集合与完整上游快照比较后产生的目录候选操作。
type ActionKind string

const (
	// Add 表示安装新对象或重新安装整个缺失的管理目录。
	Add ActionKind = "add"
	// Update 表示整体替换管理目录，而非逐文件合并。
	Update ActionKind = "update"
	// Delete 表示移除不再选中的已管理目录。
	Delete ActionKind = "delete"
	// Keep 表示不移动目录，但仍可能需要更新来源 lock。
	Keep ActionKind = "keep"
)

// LocalState 描述相对于 lock 的本地状态，指纹不包含权限和空目录。
type LocalState string

const (
	// Missing 表示整个目标不存在，不包含目录中部分文件缺失的情况。
	Missing LocalState = "missing"
	// Unmodified 表示安全目录的指纹与 lock 相同。
	Unmodified LocalState = "unmodified"
	// Modified 表示安全目录存在，但内容增删改使指纹不同。
	Modified LocalState = "modified"
	// Unmanaged 表示目标存在但当前来源没有所有权，不读取内容或尝试接管。
	Unmanaged LocalState = "unmanaged"
	// Unsafe 表示对象类型或文件树不安全，不能用 force 绕过。
	Unsafe LocalState = "unsafe"
)

// SkillAction 是候选操作。仅 Status == Ready 的来源允许执行其操作；
// Skipped/Failed 的候选操作仅供报告，不允许部署其中的任何一项。
// FromDir/Path/TargetHash 在目标中存在该 Skill 时设置；删除时为空。
// Hash 不记录权限和空目录，沿用 skills.Hash 的语义。
type SkillAction struct {
	Name       string
	Kind       ActionKind
	Local      LocalState
	Dir        string // 安装目标绝对路径。
	FromDir    string // 计划拥有的临时检出路径，Plan.Close 后不可使用。
	Path       string // 仓库内 / 相对路径。
	LockedHash string
	LocalHash  string
	TargetHash string
	Reason     string
}

// ConflictKind 区分内容保护、未管理目标、跨来源所有权和不安全文件树冲突。
type ConflictKind string

const (
	// LocalModification 表示普通同步保护已管理对象的本地修改。
	LocalModification ConflictKind = "local-modification"
	// UnmanagedName 表示目标已存在但不在当前来源 lock 中。
	UnmanagedName ConflictKind = "unmanaged-name"
	// CrossSourceName 表示多个来源的旧 lock 或目标集合声明同一名称。
	CrossSourceName ConflictKind = "cross-source-name"
	// UnsafeLocal 表示管理对象无法安全读取或计算指纹。
	UnsafeLocal ConflictKind = "unsafe-local"
)

// Conflict 记录冲突类型及涉及来源；任意一项冲突都禁止相关来源的部分部署。
type Conflict struct {
	Kind          ConflictKind
	Name          string
	SourceIndexes []int // YAML 来源顺序的零基下标；同名来源也通过下标区分。
	Reason        string
}

// SourcePlan 保留期望配置和完整目标快照，后续部署无需重新进行同步判断。
// Revision 或来源变更但文件内容相同时，可以 Ready 且全部操作为 Keep，
// 表示只需在后续部署成功时维护 lock 元数据；规划本身不写配置。
type SourcePlan struct {
	Index           int
	Source          config.Source // 独立副本，不与输入的切片或 lock map 共享。
	CurrentRevision string
	TargetRevision  string
	SourceChanged   bool
	Status          SourceStatus
	Actions         []SkillAction  // 按 Skill 名称排序。
	TargetSkills    []skills.Skill // 使用同一个 TargetRevision，按名称排序。
	Conflicts       []Conflict
	Err             error // 来源读取、Git 或发现失败；不影响其他来源。
}

// Plan 拥有各来源的候选计划与临时检出；Sources 沿用 YAML 顺序，
// Conflicts 按名称、类型和来源下标排序。
// 成功取得计划后必须 defer plan.Close()，部署也须在 Close 前消费临时快照。
// 输入、本地目录和检出目录在规划/消费期间须保持不变；不支持并发 Close。
type Plan struct {
	Sources   []SourcePlan
	Conflicts []Conflict
	checkouts []*gitrepo.Checkout
}

// Close 只清理本计划拥有的 Git 临时目录，可重复调用；清理失败可重试。
// 指针接收者直接访问计划持有的检出对象；逐个关闭并用 errors.Join 保留全部失败，
// 不因一项失败漏清理。
func (p *Plan) Close() error {
	var err error
	for _, checkout := range p.checkouts {
		err = errors.Join(err, checkout.Close())
	}
	return err
}
