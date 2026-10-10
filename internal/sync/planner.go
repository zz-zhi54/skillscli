package sync

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/zz-zhi54/skillscli/internal/config"
	gitrepo "github.com/zz-zhi54/skillscli/internal/git"
	"github.com/zz-zhi54/skillscli/internal/skills"
)

// Load 读取 YAML 及其中的 lock 后规划，不保存配置或创建安装目录。
func Load(ctx context.Context, paths config.Paths, options Options) (*Plan, error) {
	doc, err := config.Load(paths.Config)
	if err != nil {
		return nil, err
	}
	return Build(ctx, doc.Config, paths.Skills, options)
}

// Build 对每个来源检查所有已管理对象，再获取当前 source/ref 的目标版本。
// 不以 revision 相同为快捷返回条件，也不先恢复 lock 的旧 revision。
// 无效配置、安装根目录或取消返回全局错误并清理资源；来源失败保存在
// SourcePlan.Err，继续规划其他来源。成功返回后由调用方关闭 Plan。
func Build(ctx context.Context, cfg config.Config, skillsDir string, options Options) (_ *Plan, err error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	root, err := installationRoot(skillsDir)
	if err != nil {
		return nil, err
	}
	plan := &Plan{Sources: make([]SourcePlan, 0, len(cfg.Sources))}
	// 命名返回值 err 在 return 时先被赋值，defer 再合并清理错误；成功时资源交给调用方。
	defer func() {
		if err != nil {
			err = errors.Join(err, plan.Close())
		}
	}()
	for i, source := range cfg.Sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sp := SourcePlan{Index: i, Source: cloneSource(source), Status: Ready}
		if source.Lock != nil {
			sp.CurrentRevision = source.Lock.Revision
			sp.SourceChanged = source.Source != source.Lock.Source
		}
		locals, localErr := inspectManaged(root, &sp, options)
		if localErr != nil {
			sp.Err = localErr
			sp.Status = Failed
			plan.Sources = append(plan.Sources, sp)
			continue
		}
		checkout, fetchErr := gitrepo.Fetch(ctx, source.Source, source.Ref)
		if fetchErr != nil {
			sp.Err = fetchErr
			sp.Status = Failed
			plan.Sources = append(plan.Sources, sp)
			continue
		}
		plan.checkouts = append(plan.checkouts, checkout)
		sp.TargetRevision = checkout.Revision
		sp.TargetSkills, sp.Err = skills.DiscoverSnapshot(checkout.Dir, source)
		if sp.Err == nil {
			sp.Actions, sp.Err = planActions(root, &sp, locals)
		}
		if sp.Err != nil {
			sp.Status = Failed
		} else if !needsSync(sp) {
			sp.Status = Unchanged
		}
		plan.Sources = append(plan.Sources, sp)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	checkCrossSourceNames(plan)
	for i := range plan.Sources {
		sp := &plan.Sources[i]
		sortConflicts(sp.Conflicts)
		plan.Conflicts = append(plan.Conflicts, sp.Conflicts...)
		if len(sp.Conflicts) != 0 && sp.Status != Failed {
			sp.Status = Skipped
		}
	}
	// 跨来源冲突只在全局列表出现一次。
	plan.Conflicts = uniqueConflicts(plan.Conflicts)
	sortConflicts(plan.Conflicts)
	return plan, nil
}

// inspectManaged 先检查所有旧 lock 对象，包括已被 paths/exclude 或新来源排除的对象。
// 普通模式的修改冲突保护整个来源；force 只去掉 Modified 冲突，不放宽 Unsafe。
// sp 用指针传入，追加冲突直接写回调用方的来源计划，而不是修改结构体副本。
func inspectManaged(root string, sp *SourcePlan, options Options) (map[string]localSkill, error) {
	locals := make(map[string]localSkill)
	if sp.Source.Lock == nil {
		return locals, nil
	}
	for _, name := range sortedKeys(sp.Source.Lock.Skills) {
		dir, err := installationPath(root, name)
		if err != nil {
			return nil, err
		}
		local, err := inspectLocal(dir, sp.Source.Lock.Skills[name], true)
		if err != nil {
			return nil, fmt.Errorf("inspect managed Skill %q: %w", name, err)
		}
		locals[name] = local
		switch {
		case local.state == Unsafe:
			sp.Conflicts = append(sp.Conflicts, Conflict{Kind: UnsafeLocal, Name: name,
				SourceIndexes: []int{sp.Index}, Reason: local.issue})
		case local.state == Modified && !options.Force:
			sp.Conflicts = append(sp.Conflicts, Conflict{Kind: LocalModification, Name: name,
				SourceIndexes: []int{sp.Index}, Reason: "managed Skill differs from lock"})
		}
	}
	return locals, nil
}

// planActions 对旧管理集合与目标集合的并集生成稳定候选操作，而不是直接写目录。
// 即使本地冲突也保留候选，最终由来源 Status 决定能否整批执行。
func planActions(root string, sp *SourcePlan, locals map[string]localSkill) ([]SkillAction, error) {
	targets := make(map[string]skills.Skill, len(sp.TargetSkills))
	names := make(map[string]bool, len(sp.TargetSkills)+len(locals))
	for _, target := range sp.TargetSkills {
		targets[target.Name] = target
		names[target.Name] = true
	}
	for name := range locals {
		names[name] = true
	}
	actions := make([]SkillAction, 0, len(names))
	for _, name := range sortedKeys(names) {
		dir, err := installationPath(root, name)
		if err != nil {
			return nil, err
		}
		target, wanted := targets[name]
		local, managed := locals[name]
		lockedHash := ""
		if managed {
			lockedHash = sp.Source.Lock.Skills[name]
		} else {
			local, err = inspectLocal(dir, "", false)
			if err != nil {
				return nil, fmt.Errorf("inspect target Skill %q: %w", name, err)
			}
			if local.state == Unmanaged {
				sp.Conflicts = append(sp.Conflicts, Conflict{Kind: UnmanagedName, Name: name,
					SourceIndexes: []int{sp.Index}, Reason: "target path exists but is not recorded in this source lock"})
			}
		}
		action := SkillAction{Name: name, Dir: dir, Local: local.state, LocalHash: local.hash,
			LockedHash: lockedHash, FromDir: target.Dir, Path: target.Path, TargetHash: target.Hash}
		switch {
		case !wanted && local.state == Missing:
			action.Kind, action.Reason = Keep, "no longer selected and already absent"
		case !wanted:
			action.Kind, action.Reason = Delete, "no longer selected by current source/ref/paths/exclude"
		case !managed:
			action.Kind, action.Reason = Add, "not recorded in lock"
		case local.state == Missing:
			action.Kind, action.Reason = Add, "managed directory is completely missing"
		case local.state != Unmodified:
			action.Kind, action.Reason = Update, "restore managed local changes from target"
		case target.Hash != lockedHash:
			action.Kind, action.Reason = Update, "upstream fingerprint changed"
		default:
			action.Kind, action.Reason = Keep, "local and target fingerprints match lock"
		}
		actions = append(actions, action)
	}
	return actions, nil
}

// needsSync 同时比较目录操作与完整目标 lock，避免“全为 Keep”被误判为无需提交。
func needsSync(sp SourcePlan) bool {
	lock := sp.Source.Lock
	if lock == nil || sp.SourceChanged || sp.TargetRevision != sp.CurrentRevision || len(lock.Skills) != len(sp.TargetSkills) {
		return true
	}
	for _, action := range sp.Actions {
		if action.Kind != Keep {
			return true
		}
	}
	// 包括已缺失且上游删除的对象：无需删磁盘，但必须从未来 lock 中移除。
	for _, target := range sp.TargetSkills {
		if lock.Skills[target.Name] != target.Hash {
			return true
		}
	}
	return false
}

// checkCrossSourceNames 将 lock 旧所有权与完整期望集合合并检查名称碰撞。
// 即使旧来源准备删除、已缺失、被跳过或获取失败，也不自动跨来源移交所有权。
func checkCrossSourceNames(plan *Plan) {
	owners := make(map[string][]int)
	for i, sp := range plan.Sources {
		names := make(map[string]bool)
		if sp.Source.Lock != nil {
			for name := range sp.Source.Lock.Skills {
				names[name] = true
			}
		}
		for _, target := range sp.TargetSkills {
			names[target.Name] = true
		}
		for _, name := range sortedKeys(names) {
			owners[name] = append(owners[name], i)
		}
	}
	for _, name := range sortedKeys(owners) {
		indexes := owners[name]
		if len(indexes) < 2 {
			continue
		}
		conflict := Conflict{Kind: CrossSourceName, Name: name, SourceIndexes: indexes,
			Reason: "Skill name is claimed by multiple sources (lock or target)"}
		for _, i := range indexes {
			plan.Sources[i].Conflicts = append(plan.Sources[i].Conflicts, conflict)
		}
	}
}

// sortedKeys 用泛型复用不同值类型的 map 键排序，避免决策与输出依赖 map 遍历顺序。
func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// sortConflicts 原地按名称、类型和来源下标序列排序，保留 YAML 下标区分同地址来源。
func sortConflicts(conflicts []Conflict) {
	sort.Slice(conflicts, func(i, j int) bool {
		a, b := conflicts[i], conflicts[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		for k := 0; k < len(a.SourceIndexes) && k < len(b.SourceIndexes); k++ {
			if a.SourceIndexes[k] != b.SourceIndexes[k] {
				return a.SourceIndexes[k] < b.SourceIndexes[k]
			}
		}
		return len(a.SourceIndexes) < len(b.SourceIndexes)
	})
}

// uniqueConflicts 仅合并多来源各自保存的同名跨来源冲突，供全局报告输出一次。
func uniqueConflicts(conflicts []Conflict) []Conflict {
	result := make([]Conflict, 0, len(conflicts))
	seenCross := make(map[string]bool)
	for _, conflict := range conflicts {
		if conflict.Kind == CrossSourceName {
			if seenCross[conflict.Name] {
				continue
			}
			seenCross[conflict.Name] = true
		}
		result = append(result, conflict)
	}
	return result
}

// cloneConfig 深复制各来源的可变切片和 lock map，目标配置修改不污染输入或计划。
func cloneConfig(cfg config.Config) config.Config {
	cfg.Sources = append([]config.Source{}, cfg.Sources...)
	for i := range cfg.Sources {
		cfg.Sources[i] = cloneSource(cfg.Sources[i])
	}
	return cfg
}

// cloneSource 保留 nil 切片语义并复制引用数据；仅复制 Source 结构值仍会共享切片和 map。
func cloneSource(source config.Source) config.Source {
	if source.Paths != nil {
		source.Paths = append([]string{}, source.Paths...)
	}
	if source.Exclude != nil {
		source.Exclude = append([]string{}, source.Exclude...)
	}
	if source.Lock != nil {
		lock := *source.Lock
		lock.Skills = make(map[string]string, len(source.Lock.Skills))
		for name, hash := range source.Lock.Skills {
			lock.Skills[name] = hash
		}
		source.Lock = &lock
	}
	return source
}
