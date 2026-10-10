package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zz-zhi54/skillscli/internal/config"
	"github.com/zz-zhi54/skillscli/internal/skills"
)

// deploySource 校验 ready 来源及所有权，构造完整目标 lock，再交给共用事务部署。
func deploySource(ctx context.Context, paths config.Paths, doc *config.Document, plan *Plan, sp SourcePlan, expected []byte, committedConfig *[]byte, hook deployHook) (err error) {
	if err := validateSource(paths, doc, plan, sp); err != nil {
		return err
	}
	next := cloneConfig(doc.Config)
	next.Sources[sp.Index].Lock = targetLock(sp)
	return deployActions(ctx, paths, doc, sp.Actions, next, expected, committedConfig, hook)
}

// deployActions 是 sync/add/remove 共用的目录与 YAML 事务；调用方负责业务规划和所有权校验。
// 临时目录置于作用域内，依赖同一文件系统上的 rename；备份、安装、配置保存、提交按此顺序进行。
// committedConfig 仅在提交标记发布后写出真实 after 字节，即使随后清理失败也能推进基线。
func deployActions(ctx context.Context, paths config.Paths, doc *config.Document, actions []SkillAction, next config.Config, expected []byte, committedConfig *[]byte, hook deployHook) (err error) {
	if err := checkConfig(paths.Config, expected); err != nil {
		return err
	}
	agentsInfo, err := os.Lstat(filepath.Dir(paths.Config))
	if err != nil {
		return err
	}
	skillsInfo, err := os.Lstat(paths.Skills)
	if err != nil {
		return err
	}
	checkRoots := func() error {
		for path, original := range map[string]os.FileInfo{filepath.Dir(paths.Config): agentsInfo, paths.Skills: skillsInfo} {
			current, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !current.IsDir() || !os.SameFile(current, original) {
				return errors.New("deployment root changed")
			}
		}
		return nil
	}
	if err := checkRoots(); err != nil {
		return err
	}
	for _, action := range actions {
		if err := checkLocal(action); err != nil {
			return err
		}
	}
	txn, err := os.MkdirTemp(filepath.Dir(paths.Config), ".skillscli-txn-")
	if err != nil {
		return err
	}
	// started 表示 journal 已发布，之后才可能移动正式对象；committed 表示提交标记已发布。
	started, committed := false, false
	// 命名 err 使 defer 能决定回滚并返回清理错误；回滚不用 ctx，取消不能半途丢弃唯一备份。
	defer func() {
		if err != nil && started && !committed {
			recoveryErr := rollback(paths, txn)
			if recoveryErr != nil {
				err = errors.Join(err, &RecoveryError{Dir: txn, Err: recoveryErr})
				return
			}
			restored, loadErr := config.Load(paths.Config)
			if loadErr != nil {
				err = errors.Join(err, &RecoveryError{Dir: txn, Err: errors.New("cannot reload restored configuration")})
				return
			}
			*doc = *restored
		}
		// 没有 journal 时尚未修改正式文件；成功提交或完整恢复后才清理备份。
		err = errors.Join(err, cleanupTransaction(paths, txn))
	}()
	for _, dir := range []string{"new", "old"} {
		if err := os.Mkdir(filepath.Join(txn, dir), 0700); err != nil {
			return err
		}
	}
	for _, action := range actions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if action.Kind != Add && action.Kind != Update {
			continue
		}
		if hook != nil {
			if err := hook("prepare", action.Name); err != nil {
				return err
			}
		}
		dst := filepath.Join(txn, "new", action.Name)
		if err := copySkill(action.FromDir, dst); err != nil {
			return fmt.Errorf("prepare Skill %q: %w", action.Name, err)
		}
		name, err := skills.Validate(dst)
		if err != nil {
			return err
		}
		hash, err := skills.Hash(dst)
		if err != nil {
			return err
		}
		if name != action.Name || hash != action.TargetHash {
			return fmt.Errorf("prepared Skill %q differs from target snapshot", action.Name)
		}
	}
	// 临时切换模型只为预编码 after；恢复原模型，正式发布仍由后面的 Save 完成。
	beforeConfig := doc.Config
	doc.Config = next
	after, encodeErr := doc.Bytes()
	doc.Config = beforeConfig
	if encodeErr != nil {
		return encodeErr
	}
	journal := transaction{Version: 1, BeforeExists: expected != nil, BeforeHash: digest(expected), AfterHash: digest(after)}
	for _, action := range actions {
		if action.Kind == Keep {
			continue
		}
		journal.Actions = append(journal.Actions, transactionAction{Name: action.Name, Kind: action.Kind, OldHash: action.LocalHash, NewHash: action.TargetHash})
	}
	if err := writeSynced(filepath.Join(txn, "config.before"), expected, 0600); err != nil {
		return err
	}
	if err := writeSynced(filepath.Join(txn, "config.after"), after, 0600); err != nil {
		return err
	}
	if err := writeJournal(txn, journal); err != nil {
		return err
	}
	started = true
	if hook != nil {
		if err := hook("prepared", ""); err != nil {
			return err
		}
	}
	// 全部准备完成后再次验证，force 也不能放宽与计划快照的比较。
	if err := checkRoots(); err != nil {
		return err
	}
	if err := checkConfig(paths.Config, expected); err != nil {
		return err
	}
	for _, action := range actions {
		if err := checkLocal(action); err != nil {
			return err
		}
	}
	// 完整备份后才安装任何新版目录，Windows 不尝试覆盖非空目录。
	for _, action := range actions {
		if action.Kind != Update && action.Kind != Delete {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkRoots(); err != nil {
			return err
		}
		if err := checkLocal(action); err != nil {
			return err
		}
		if hook != nil {
			if err := hook("backup", action.Name); err != nil {
				return err
			}
		}
		backup := filepath.Join(txn, "old", action.Name)
		if err := renameAbsent(action.Dir, backup); err != nil {
			return err
		}
		hash, err := skills.Hash(backup)
		if err != nil {
			return err
		}
		if hash != action.LocalHash {
			return fmt.Errorf("Skill %q changed during backup", action.Name)
		}
	}
	for _, action := range actions {
		if action.Kind != Add && action.Kind != Update {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkRoots(); err != nil {
			return err
		}
		if hook != nil {
			if err := hook("install", action.Name); err != nil {
				return err
			}
		}
		if err := renameAbsent(filepath.Join(txn, "new", action.Name), action.Dir); err != nil {
			return err
		}
	}
	// 只提交实际安装的目标指纹；keep 也重新验证，防止提交错误的 lock。
	for _, action := range actions {
		if action.TargetHash == "" {
			if _, err := os.Lstat(action.Dir); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("removed Skill %q is no longer absent", action.Name)
			}
			continue
		}
		hash, err := skills.Hash(action.Dir)
		if err != nil {
			return err
		}
		if hash != action.TargetHash {
			return fmt.Errorf("installed Skill %q changed before lock commit", action.Name)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkRoots(); err != nil {
		return err
	}
	doc.Config = next
	if hook != nil {
		if err := hook("save", ""); err != nil {
			return err
		}
	}
	if err := checkConfig(paths.Config, expected); err != nil {
		return err
	}
	if err := doc.Save(paths.Config); err != nil {
		return err
	}
	if hook != nil {
		if err := hook("saved", ""); err != nil {
			return err
		}
	}
	// YAML 保存还不代表事务提交；标记须先完成 Sync/Close 并原子发布，失败仍回滚。
	if err := config.WriteAtomic(filepath.Join(txn, "committed"), []byte("committed\n")); err != nil {
		return err
	}
	committed = true
	if committedConfig != nil {
		*committedConfig = after
	}
	return nil
}

// copySkill 复制安全快照的原始字节和普通文件权限，不克隆 ACL/扩展属性、不执行脚本。
// 整体准备完还要核对名称与指纹，复制期间的路径检查不能完全排除外部恶意替换竞态。
func copySkill(src, dst string) error {
	// 先复用安全检查，复制时仍拒绝链接及特殊文件。内容原样复制，保留执行位。
	if _, err := skills.Hash(src); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.Mkdir(target, 0700)
		}
		if !info.Mode().IsRegular() {
			return errors.New("unsafe file in Skill snapshot")
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		// defer 属于这次 WalkDir 回调，每个文件处理完即关闭，不会积累到整棵树遍历结束。
		defer input.Close()
		actual, err := input.Stat()
		current, statErr := os.Lstat(path)
		if err != nil || statErr != nil || !current.Mode().IsRegular() || !os.SameFile(actual, current) {
			return errors.New("snapshot file changed while opening")
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		if copyErr == nil {
			copyErr = output.Sync()
		}
		return errors.Join(copyErr, output.Close())
	})
}

// renameAbsent 拒绝覆盖任何已存在目标（含断链），让备份、安装、回滚使用同一安全约束。
// Lstat 与 Rename 之间仍有命名空间竞态，作用域锁不约束外部程序。
func renameAbsent(from, to string) error {
	if _, err := os.Lstat(to); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return errors.New("rename destination already exists")
	}
	return os.Rename(from, to)
}

// writeSynced 排他创建事务快照，写入后 Sync 并关闭，保留写入/同步与关闭的全部错误。
// 不同步父目录，因此不承诺突然断电下的持久原子性。
func writeSynced(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
