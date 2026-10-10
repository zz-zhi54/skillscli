package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zz-zhi54/skillscli/internal/config"
)

var (
	// ErrSourceExists 表示作用域内已有完全相同的 source 字符串，更新应使用 sync。
	ErrSourceExists = errors.New("source already exists; use sync to update it")
	// ErrNoSkills 表示 add 的选中集合为空，不能持久化空的新来源。
	ErrNoSkills = errors.New("no Skills found in selected paths")
	// ErrNotManaged 表示指定名称未出现在当前作用域的任何 lock 中。
	ErrNotManaged = errors.New("Skill is not managed in this scope")
	// ErrModified 表示普通 remove 拒绝删除相对 lock 已修改的对象。
	ErrModified = errors.New("Skill has local modifications; use --force to remove it")
	// ErrUnsafe 表示 remove 无法证明本地文件树可安全删除，force 也不放宽。
	ErrUnsafe = errors.New("unsafe local Skill path; refusing removal")
	// ErrOwnership 表示名称有多个 lock 所有者或大小写别名，不能安全定位删除对象。
	ErrOwnership = errors.New("Skill name is claimed by multiple sources")
)

// AddSource 只获取新来源，旧来源仅提供 lock 所有权，不顺带同步或修复它们。
// selectedPaths 为 nil 时默认 skills，显式空切片由配置校验拒绝；成功部署才保存 source+lock。
func AddSource(ctx context.Context, paths config.Paths, source string, selectedPaths []string) (Result, error) {
	return addSource(ctx, paths, source, selectedPaths, nil)
}

// addSource 在持锁并恢复后只 Build 新来源，再把已有 lock 所有权加入全局碰撞检查。
func addSource(ctx context.Context, paths config.Paths, source string, selectedPaths []string, hook deployHook) (result Result, err error) {
	paths, doc, expected, lock, err := openScope(paths)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	for _, existing := range doc.Config.Sources {
		if existing.Source == source {
			return result, ErrSourceExists
		}
	}
	if selectedPaths == nil {
		selectedPaths = []string{"skills"}
	}
	added := config.Source{Source: source, Paths: selectedPaths}
	plan, err := Build(ctx, config.Config{Version: config.Version, Sources: []config.Source{added}}, paths.Skills, Options{})
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, plan.Close()) }()
	sp := plan.Sources[0]
	sp.Index = len(doc.Config.Sources)
	for i := range sp.Conflicts {
		sp.Conflicts[i].SourceIndexes = []int{sp.Index}
	}
	plan.Sources = nil
	for i, existing := range doc.Config.Sources {
		plan.Sources = append(plan.Sources, SourcePlan{Index: i, Source: cloneSource(existing), Status: Unchanged})
	}
	plan.Sources = append(plan.Sources, sp)
	checkCrossSourceNames(plan)
	sp = plan.Sources[sp.Index]
	sortConflicts(sp.Conflicts)
	if len(sp.Conflicts) != 0 && sp.Status != Failed {
		sp.Status = Skipped
	}
	result = Result{Source: sp, Status: sp.Status, Err: sp.Err}
	if sp.Status == Failed {
		return result, sp.Err
	}
	if sp.Status == Skipped {
		result.Err = errors.New("source has unresolved conflicts")
		return result, result.Err
	}
	if len(sp.TargetSkills) == 0 {
		result.Status, result.Err = Failed, ErrNoSkills
		return result, result.Err
	}
	// 新来源只在内存追加，deploySource 将来源和 lock 一起纳入原有 YAML 事务。
	doc.Config.Sources = append(doc.Config.Sources, cloneSource(added))
	result.Err = deploySource(ctx, paths, doc, plan, sp, expected, nil, hook)
	if result.Err != nil {
		result.Status = Failed
	}
	return result, result.Err
}

// RemoveSkill 不获取上游、不检查或覆盖同来源其他 Skill 的本地内容。
// 保留来源时更新 lock 并补入去重 exclude；移除最后一个管理对象时删除整个来源记录。
func RemoveSkill(ctx context.Context, paths config.Paths, name string, options Options) error {
	return removeSkill(ctx, paths, name, options, nil)
}

// removeSkill 先按 lock 唯一定位所有者并检查大小写别名，再只为该名称建立删除事务。
func removeSkill(ctx context.Context, paths config.Paths, name string, options Options, hook deployHook) (err error) {
	paths, doc, expected, lock, err := openScope(paths)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	dir, err := installationPath(paths.Skills, name)
	if err != nil {
		return err
	}
	if err := portableName(name); err != nil {
		return err
	}
	owner := -1
	for i, source := range doc.Config.Sources {
		if source.Lock != nil {
			if _, exists := source.Lock.Skills[name]; exists {
				if owner != -1 {
					return ErrOwnership
				}
				owner = i
			}
		}
	}
	if owner == -1 {
		return ErrNotManaged
	}
	for i, source := range doc.Config.Sources {
		if source.Lock == nil {
			continue
		}
		for other := range source.Lock.Skills {
			if (i != owner || other != name) && strings.EqualFold(other, name) {
				return ErrOwnership
			}
		}
	}
	lockedHash := doc.Config.Sources[owner].Lock.Skills[name]
	local, err := inspectLocal(dir, lockedHash, true)
	if err != nil {
		return err
	}
	if local.state == Unsafe {
		return ErrUnsafe
	}
	if local.state == Modified && !options.Force {
		return ErrModified
	}
	action := SkillAction{Name: name, Dir: dir, Kind: Delete, Local: local.state, LocalHash: local.hash, LockedHash: lockedHash}
	if local.state == Missing {
		// 磁盘已无目录，仍需事务提交 lock/exclude 或来源删除，不能直接当作无操作返回。
		action.Kind = Keep
	}
	next := cloneConfig(doc.Config)
	source := &next.Sources[owner]
	delete(source.Lock.Skills, name)
	if len(source.Lock.Skills) == 0 {
		if err := doc.RemoveSource(owner); err != nil {
			return err
		}
		next = cloneConfig(doc.Config)
	} else {
		// lock 不记录发现方式；对扫描和显式 Skill 路径统一排除，避免下次 sync 重装。
		found := false
		for _, excluded := range source.Exclude {
			found = found || excluded == name
		}
		if !found {
			source.Exclude = append(source.Exclude, name)
		}
	}
	return deployActions(ctx, paths, doc, []SkillAction{action}, next, expected, nil, hook)
}

// ManagedSkill 是 list 的只读管理记录，按来源顺序及 Skill 名称稳定排列。
// State 可为 Unsafe，指纹检查失败不能被误报为未修改；不推断上游是否有更新。
type ManagedSkill struct {
	Name        string
	Source      string // 实际安装来源（lock.source），不是尚未同步的期望来源。
	SourceIndex int
	State       LocalState
}

// ListManaged 只读 YAML 和本地指纹，不加写锁、不恢复事务、不创建路径、不访问 Git。
func ListManaged(paths config.Paths) ([]ManagedSkill, error) {
	expected, err := readConfig(paths.Config)
	if err != nil {
		return nil, err
	}
	doc, err := config.Load(paths.Config)
	if err != nil {
		return nil, err
	}
	root, err := installationRoot(paths.Skills)
	if err != nil {
		return nil, err
	}
	var result []ManagedSkill
	for i, source := range doc.Config.Sources {
		if source.Lock == nil {
			continue
		}
		for _, name := range sortedKeys(source.Lock.Skills) {
			dir, err := installationPath(root, name)
			if err != nil {
				return nil, err
			}
			local, err := inspectLocal(dir, source.Lock.Skills[name], true)
			if err != nil {
				return nil, fmt.Errorf("inspect managed Skill: %w", err)
			}
			result = append(result, ManagedSkill{Name: name, Source: source.Lock.Source, SourceIndex: i, State: local.state})
		}
	}
	if err := checkConfig(paths.Config, expected); err != nil {
		return nil, err
	}
	return result, nil
}
