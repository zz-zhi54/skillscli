package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/zz-zhi54/skillscli/internal/config"
	"github.com/zz-zhi54/skillscli/internal/skills"
)

// Result 汇总单来源的规划与执行结果；清理失败也会记为 Failed，不等于部署一定未提交。
// Source 中的 FromDir 只供报告，Run 返回前已尝试关闭计划，不能将这些路径当作后续可用快照。
type Result struct {
	Source SourcePlan
	Status SourceStatus
	Err    error
}

// Run 持有作用域锁，从恢复、规划到部署及检出清理都在同一临界区内。
// ctx 传给 Git 获取和部署检查点；取消后仍执行不受 ctx 取消影响的回滚与清理。
func Run(ctx context.Context, paths config.Paths, options Options) (results []Result, err error) {
	paths, doc, expected, lock, err := openScope(paths)
	if err != nil {
		return nil, err
	}
	// 命名返回值让 defer 能保留业务错误并合并关闭错误；LIFO 保证先关 Plan、后释放锁。
	defer func() { err = errors.Join(err, lock.Close()) }()
	plan, err := Build(ctx, doc.Config, paths.Skills, options)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, plan.Close()) }()
	if err := checkConfig(paths.Config, expected); err != nil {
		return nil, err
	}
	return execute(ctx, paths, doc, plan, expected, nil)
}

// openScope 共用写命令的作用域锁、恢复和 YAML 字节基线；成功后调用方负责关闭锁。
func openScope(paths config.Paths) (_ config.Paths, _ *config.Document, _ []byte, lock *os.File, err error) {
	paths, err = deploymentPaths(paths)
	if err != nil {
		return paths, nil, nil, nil, err
	}
	lock, err = acquireScope(filepath.Dir(paths.Config))
	if err != nil {
		return paths, nil, nil, nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, lock.Close())
		}
	}()
	if err := recoverTransactions(paths); err != nil {
		return paths, nil, nil, lock, err
	}
	expected, err := readConfig(paths.Config)
	if err != nil {
		return paths, nil, nil, lock, err
	}
	doc, err := config.Load(paths.Config)
	if err != nil {
		return paths, nil, nil, lock, err
	}
	if err := checkConfig(paths.Config, expected); err != nil {
		return paths, nil, nil, lock, err
	}
	return paths, doc, expected, lock, nil
}

// hook 仅用于在项目拥有的事务边界注入故障，不替换标准库行为。
type deployHook func(stage, name string) error

// execute 按 YAML 顺序仅部署 Ready 来源，汇总独立失败而不撤销先前来源的成功。
// 未恢复的活跃事务会阻止后续提交；已提交的终态垃圾清理失败不破坏 YAML 基线。
func execute(ctx context.Context, paths config.Paths, doc *config.Document, plan *Plan, expected []byte, hook deployHook) ([]Result, error) {
	results := make([]Result, 0, len(plan.Sources))
	var failures error
	for _, sp := range plan.Sources {
		result := Result{Source: sp, Status: sp.Status, Err: sp.Err}
		var committedConfig []byte
		switch sp.Status {
		case Ready:
			result.Err = deploySource(ctx, paths, doc, plan, sp, expected, &committedConfig, hook)
			if result.Err != nil {
				result.Status = Failed
			}
			if committedConfig != nil {
				// 只推进真正提交的字节基线，失败回滚保留原始 YAML 格式。
				expected = committedConfig
			}
		case Unchanged:
		case Skipped:
			result.Err = errors.New("source has unresolved conflicts")
		case Failed:
			if result.Err == nil {
				result.Err = errors.New("source planning failed")
			}
		default:
			result.Status = Failed
			result.Err = errors.New("invalid source plan status")
		}
		results = append(results, result)
		if result.Err != nil {
			failures = errors.Join(failures, fmt.Errorf("source %d: %w", sp.Index+1, result.Err))
		}
		// 恢复失败时不能继续发布另一来源的 YAML，否则会失去可识别的恢复基线。
		var recovery *RecoveryError
		if errors.As(result.Err, &recovery) && committedConfig == nil && !strings.HasPrefix(filepath.Base(recovery.Dir), ".skillscli-cleanup-") {
			break
		}
	}
	return results, failures
}

// deploymentPaths 为写命令建立真实目录并规范化共同父路径，为同盘 rename 布置目标与备份。
// 配置与 skills 必须为同一作用域内的固定文件名；Lstat 拒绝作用域根和安装根自身为链接。
func deploymentPaths(paths config.Paths) (config.Paths, error) {
	var err error
	paths.Config, err = filepath.Abs(paths.Config)
	if err != nil {
		return paths, err
	}
	paths.Skills, err = filepath.Abs(paths.Skills)
	if err != nil {
		return paths, err
	}
	agents := filepath.Dir(paths.Config)
	if filepath.Dir(paths.Skills) != agents || filepath.Base(paths.Skills) != "skills" || filepath.Base(paths.Config) != "skillscli.yaml" {
		return paths, errors.New("deployment requires config and skills in the same .agents scope")
	}
	if err := ensureDirectory(agents); err != nil {
		return paths, err
	}
	agents, err = filepath.EvalSymlinks(agents)
	if err != nil {
		return paths, err
	}
	paths.Config = filepath.Join(agents, "skillscli.yaml")
	paths.Skills = filepath.Join(agents, "skills")
	if err := ensureDirectory(paths.Skills); err != nil {
		return paths, err
	}
	return paths, nil
}

// ensureDirectory 仅创建当前一级目录并用 Lstat 验证，不接受符号链接充当真实根。
func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a real directory", path)
	}
	return nil
}

// readConfig 只读取普通文件；nil 表示不存在，与已存在的空文件字节区分。
func readConfig(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("config is not a regular file")
	}
	return os.ReadFile(path)
}

// checkConfig 对比原始字节及存在性，用户只修改注释也会使写事务拒绝过期基线。
func checkConfig(path string, expected []byte) error {
	current, err := readConfig(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, expected) || (current == nil) != (expected == nil) {
		return errors.New("configuration changed after planning")
	}
	return nil
}

// validateSource 验证计划的完整性和仍然有效的所有权，不决定 add/update/delete。
func validateSource(paths config.Paths, doc *config.Document, plan *Plan, sp SourcePlan) error {
	if sp.Status != Ready || len(sp.Conflicts) != 0 || sp.Err != nil {
		return errors.New("source plan is not ready")
	}
	if sp.Index < 0 || sp.Index >= len(doc.Config.Sources) || !reflect.DeepEqual(doc.Config.Sources[sp.Index], sp.Source) {
		return errors.New("source ownership or configuration changed after planning")
	}
	target := make(map[string]skills.Skill)
	for _, skill := range sp.TargetSkills {
		if _, exists := target[skill.Name]; exists {
			return errors.New("duplicate target in plan")
		}
		target[skill.Name] = skill
	}
	seen := make(map[string]bool)
	for _, action := range sp.Actions {
		dir, err := installationPath(paths.Skills, action.Name)
		if err != nil {
			return err
		}
		if err := portableName(action.Name); err != nil {
			return err
		}
		if dir != action.Dir || seen[action.Name] {
			return errors.New("invalid or duplicate planned destination")
		}
		seen[action.Name] = true
		locked := ""
		if sp.Source.Lock != nil {
			locked = sp.Source.Lock.Skills[action.Name]
		}
		if locked != action.LockedHash {
			return errors.New("planned ownership does not match lock")
		}
		for _, other := range plan.Sources {
			if other.Index == sp.Index {
				continue
			}
			if other.Source.Lock != nil {
				for name := range other.Source.Lock.Skills {
					if strings.EqualFold(name, action.Name) {
						return errors.New("destination is claimed by another source")
					}
				}
			}
			for _, skill := range other.TargetSkills {
				if strings.EqualFold(skill.Name, action.Name) {
					return errors.New("destination is claimed by another source")
				}
			}
		}
		for name := range seen {
			if name != action.Name && strings.EqualFold(name, action.Name) {
				return errors.New("destinations alias on case-insensitive filesystems")
			}
		}
		wanted, exists := target[action.Name]
		if exists {
			if action.TargetHash != wanted.Hash || action.FromDir != wanted.Dir || action.Path != wanted.Path {
				return errors.New("action differs from target snapshot")
			}
			delete(target, action.Name)
		} else if action.TargetHash != "" || action.FromDir != "" {
			return errors.New("action has no target snapshot")
		}
		switch action.Kind {
		case Add:
			if !exists || action.Local != Missing {
				return errors.New("unsafe add plan")
			}
		case Update:
			if !exists || locked == "" || (action.Local != Modified && action.Local != Unmodified) {
				return errors.New("unsafe update plan")
			}
		case Delete:
			if exists || locked == "" || (action.Local != Modified && action.Local != Unmodified) {
				return errors.New("unsafe delete plan")
			}
		case Keep:
			if (exists && (locked == "" || action.Local != Unmodified || action.LocalHash != wanted.Hash)) || (!exists && action.Local != Missing) {
				return errors.New("unsafe keep plan")
			}
		default:
			return errors.New("unknown planned action")
		}
	}
	if len(target) != 0 {
		return errors.New("target missing from actions")
	}
	if sp.Source.Lock != nil {
		for name := range sp.Source.Lock.Skills {
			if !seen[name] {
				return errors.New("managed Skill missing from actions")
			}
		}
	}
	lock := targetLock(sp)
	cfg := config.Config{Version: config.Version, Sources: []config.Source{{Source: sp.Source.Source, Lock: lock}}}
	return cfg.Validate()
}

// portableName 补充跨平台保留名与字符限制；基础单组件路径检查由 installationPath 完成。
func portableName(name string) error {
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") || strings.ContainsAny(name, "<>\"|?*") {
		return fmt.Errorf("Skill %q is not a portable directory name", name)
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return fmt.Errorf("Skill %q is a reserved directory name", name)
	}
	return nil
}

// targetLock 用实际检出 revision 和完整目标集合生成新 lock，不沿用旧选择集合。
func targetLock(sp SourcePlan) *config.Lock {
	lock := &config.Lock{Source: sp.Source.Source, Revision: sp.TargetRevision, Skills: make(map[string]string)}
	for _, skill := range sp.TargetSkills {
		lock.Skills[skill.Name] = skill.Hash
	}
	return lock
}

// checkLocal 重新比较规划时的存在状态与指纹，不仅比较 lock；force 不能覆盖规划后的新修改。
func checkLocal(action SkillAction) error {
	local, err := inspectLocal(action.Dir, action.LockedHash, action.LockedHash != "")
	if err != nil {
		return err
	}
	if local.state != action.Local || local.hash != action.LocalHash || local.state == Unsafe || local.state == Unmanaged {
		return fmt.Errorf("Skill %q changed after planning; refusing stale plan", action.Name)
	}
	return nil
}
