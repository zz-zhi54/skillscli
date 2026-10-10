package sync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/zz-zhi54/skillscli/internal/config"
	"github.com/zz-zhi54/skillscli/internal/skills"
)

// transaction 是移动正式目录前发布的不可变日志；进度由 old/new/正式位置推断。
// BeforeExists 区分不存在的 YAML 与已有空文件，摘要本身无法区分这两种状态。
type transaction struct {
	Version      int                 `json:"version"`
	BeforeExists bool                `json:"before_exists"`
	BeforeHash   string              `json:"before_hash"`
	AfterHash    string              `json:"after_hash"`
	Actions      []transactionAction `json:"actions"`
}

type transactionAction struct {
	Name    string     `json:"name"`
	Kind    ActionKind `json:"kind"`
	OldHash string     `json:"old_hash"`
	NewHash string     `json:"new_hash"`
}

// RecoveryError 表示仍需恢复或清理的本地事务，Dir 必须保留给人工处理。
// Err 只包含恢复步骤的本地路径/安全原因，不包含来源地址或 YAML 原文。
type RecoveryError struct {
	Dir string
	Err error
}

// Error 返回人工恢复所需的本地保留路径和原因；指针接收者与 errors.As 的类型匹配。
func (e *RecoveryError) Error() string {
	return fmt.Sprintf("automatic recovery failed; recovery data retained at %q: %v", e.Dir, e.Err)
}

// Unwrap 保留底层原因，允许 errors.Is/As 穿过人工恢复错误继续判断。
func (e *RecoveryError) Unwrap() error { return e.Err }

// digest 校验事务 YAML 的原始字节，不把注释、空白变化视为同一配置。
func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeJournal 在正式目录移动前原子发布不可变日志，不逐操作更新进度。
func writeJournal(dir string, journal transaction) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return config.WriteAtomic(filepath.Join(dir, "journal.json"), data)
}

// readJournal 先拒绝不安全文件树，再严格解析唯一 JSON 值并核对 YAML 摘要和操作字段。
// 日志与快照损坏时停止恢复，不能凭残缺信息删除备份或发布旧 lock。
func readJournal(dir string) (transaction, error) {
	var journal transaction
	if err := safeTransactionTree(dir); err != nil {
		return journal, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		return journal, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return journal, errors.New("invalid transaction journal")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return journal, errors.New("invalid trailing transaction data")
	}
	if journal.Version != 1 {
		return journal, errors.New("unsupported transaction journal")
	}
	before, err := os.ReadFile(filepath.Join(dir, "config.before"))
	if err != nil {
		return journal, err
	}
	after, err := os.ReadFile(filepath.Join(dir, "config.after"))
	if err != nil {
		return journal, err
	}
	if digest(before) != journal.BeforeHash || digest(after) != journal.AfterHash {
		return journal, errors.New("transaction config snapshot is damaged")
	}
	seen := make(map[string]bool)
	for _, action := range journal.Actions {
		if _, err := installationPath(dir, action.Name); err != nil {
			return journal, err
		}
		if err := portableName(action.Name); err != nil {
			return journal, err
		}
		if seen[action.Name] {
			return journal, errors.New("duplicate transaction action")
		}
		seen[action.Name] = true
		if action.Kind != Add && action.Kind != Update && action.Kind != Delete {
			return journal, errors.New("invalid transaction action")
		}
		if (action.Kind == Add && action.OldHash != "") || (action.Kind == Delete && action.NewHash != "") ||
			((action.Kind == Add || action.Kind == Update) && !validFingerprint(action.NewHash)) ||
			((action.Kind == Update || action.Kind == Delete) && !validFingerprint(action.OldHash)) {
			return journal, errors.New("invalid transaction fingerprint")
		}
	}
	return journal, nil
}

// validFingerprint 借用配置锁的既有校验规则，不另造一套 h1 指纹解析器。
func validFingerprint(hash string) bool {
	cfg := config.Config{Version: config.Version, Sources: []config.Source{{Source: "transaction", Lock: &config.Lock{Source: "transaction", Revision: strings.Repeat("0", 40), Skills: map[string]string{"skill": hash}}}}}
	return cfg.Validate() == nil
}

// safeTransactionTree 拒绝事务目录中的所有链接和特殊文件，防止恢复读取逃逸目标。
func safeTransactionTree(dir string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("unsafe object in recovery directory %q", path)
		}
		return nil
	})
}

// recoverTransactions 在写命令规划前处理本作用域的中断事务，调用方必须先持有作用域锁。
// 完整提交标记只允许清理；没有有效标记就回滚。终态 cleanup 不再解析旧日志或回滚。
func recoverTransactions(paths config.Paths) error {
	entries, err := os.ReadDir(filepath.Dir(paths.Config))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".skillscli-txn-") && !strings.HasPrefix(entry.Name(), ".skillscli-cleanup-") {
			continue
		}
		dir := filepath.Join(filepath.Dir(paths.Config), entry.Name())
		if !entry.IsDir() {
			return &RecoveryError{Dir: dir, Err: errors.New("transaction path is not a real directory")}
		}
		if strings.HasPrefix(entry.Name(), ".skillscli-cleanup-") {
			if err := cleanupTransaction(paths, dir); err != nil {
				return err
			}
			continue
		}
		if _, err := os.Lstat(filepath.Join(dir, "journal.json")); errors.Is(err, os.ErrNotExist) {
			// 初始日志发布之前只能准备 new 和配置快照，尚未移动正式目录。
			if err := safeTransactionTree(dir); err != nil {
				return &RecoveryError{Dir: dir, Err: err}
			}
			old, err := os.ReadDir(filepath.Join(dir, "old"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return &RecoveryError{Dir: dir, Err: err}
			}
			if len(old) != 0 {
				return &RecoveryError{Dir: dir, Err: errors.New("journal is missing but original backups exist")}
			}
			if err := cleanupTransaction(paths, dir); err != nil {
				return err
			}
			continue
		}
		if _, err := readJournal(dir); err != nil {
			return &RecoveryError{Dir: dir, Err: err}
		}
		marker, markerErr := os.ReadFile(filepath.Join(dir, "committed"))
		if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
			return &RecoveryError{Dir: dir, Err: markerErr}
		}
		if !bytes.Equal(marker, []byte("committed\n")) {
			if err := rollback(paths, dir); err != nil {
				return &RecoveryError{Dir: dir, Err: err}
			}
		}
		if err := cleanupTransaction(paths, dir); err != nil {
			return err
		}
	}
	return nil
}

// rollback 在 YAML 仍匹配 before/after 时逆序恢复所有目录，全部成功才恢复原始配置。
// 不接收 context：取消只停止前向部署，不能中断恢复；多个目录错误以 errors.Join 保留供重试。
func rollback(paths config.Paths, dir string) error {
	journal, err := readJournal(dir)
	if err != nil {
		return err
	}
	for _, root := range []string{filepath.Dir(paths.Config), paths.Skills} {
		info, err := os.Lstat(root)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("recovery root is not a real directory")
		}
	}
	// 非本事务写入的配置不能被恢复覆盖（例如用户在中断后手动编辑）。
	current, err := readConfig(paths.Config)
	if err != nil {
		return err
	}
	isBefore := digest(current) == journal.BeforeHash && (current != nil) == journal.BeforeExists
	isAfter := current != nil && digest(current) == journal.AfterHash
	if !isBefore && !isAfter {
		return errors.New("configuration changed outside the transaction; refusing to overwrite it")
	}
	var failures error
	for i := len(journal.Actions) - 1; i >= 0; i-- {
		action := journal.Actions[i]
		if err := rollbackAction(paths.Skills, dir, action); err != nil {
			failures = errors.Join(failures, fmt.Errorf("restore Skill %q: %w", action.Name, err))
		}
	}
	if failures != nil {
		return failures
	}
	if isAfter && !isBefore {
		before, err := os.ReadFile(filepath.Join(dir, "config.before"))
		if err != nil {
			return err
		}
		if journal.BeforeExists {
			return config.WriteAtomic(paths.Config, before)
		}
		// 只移走经过指纹验证的本次 YAML；不会删除未验证的配置路径。
		if err := checkConfig(paths.Config, current); err != nil {
			return err
		}
		return renameAbsent(paths.Config, filepath.Join(dir, "config.uncommitted"))
	}
	return nil
}

// present 用 Lstat 判断位置是否占用，断开的符号链接也不是可安全覆盖的空位置。
func present(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// rollbackAction 从 new、old 和正式目录的位置组合推断操作进度，支持回滚再中断后的重入。
// 要移走正式新版须先匹配 NewHash；恢复备份绝不覆盖已有对象，也不删除唯一原目录。
func rollbackAction(root, dir string, action transactionAction) error {
	target := filepath.Join(root, action.Name)
	backup := filepath.Join(dir, "old", action.Name)
	staged := filepath.Join(dir, "new", action.Name)
	hasBackup, err := present(backup)
	if err != nil {
		return err
	}
	hasNew, err := present(staged)
	if err != nil {
		return err
	}
	hasTarget, err := present(target)
	if err != nil {
		return err
	}
	// new 不在事务目录表示新版已移入正式位置。回滚也通过 rename 搬回，
	// 这样再次中断可由同一组路径位置推断进度，不需逐操作更新日志。
	if action.Kind != Delete && !hasNew {
		if !hasTarget {
			return errors.New("installed directory and staged copy are both missing")
		}
		hash, err := skills.Hash(target)
		if err != nil {
			return err
		}
		if hash != action.NewHash {
			return errors.New("installed directory was modified; refusing to discard it")
		}
		if action.Kind == Update && !hasBackup {
			return errors.New("original backup is missing")
		}
		if err := renameAbsent(target, staged); err != nil {
			return err
		}
		hasTarget = false
	}
	if hasBackup {
		if hasTarget {
			return errors.New("original backup cannot be restored over an existing object")
		}
		// 原目录在检查和备份之间发生变化时也完整搬回，绝不删除唯一备份。
		return renameAbsent(backup, target)
	}
	// 没有备份时，更新/删除仍必须有原目录；否则不能声称已恢复并清理日志。
	if action.Kind != Add && !hasTarget {
		return errors.New("original directory and backup are both missing")
	}
	// 尚未备份或已完整恢复；不覆盖用户新改动。
	return nil
}

// cleanupTransaction 仅清理已证明可丢弃的作用域事务，调用方须确认尚未触碰正式对象，
// 或已完成提交、回滚；本函数不自行判定事务能否丢弃。
// 先改名进入 cleanup 终态再删除，支持部分删除后重试；命名 err 让 defer 报告实际保留路径。
func cleanupTransaction(paths config.Paths, dir string) (err error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	name := filepath.Base(absolute)
	if filepath.Dir(absolute) != filepath.Dir(paths.Config) || (!strings.HasPrefix(name, ".skillscli-txn-") && !strings.HasPrefix(name, ".skillscli-cleanup-")) {
		return errors.New("refusing cleanup outside scope transaction directory")
	}
	defer func() {
		if err != nil {
			err = &RecoveryError{Dir: absolute, Err: err}
		}
	}()
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("refusing cleanup of non-directory transaction path")
	}
	if strings.HasPrefix(name, ".skillscli-txn-") {
		// 进入终态后先原子改名；递归删除中断也不再被误认为待回滚事务。
		garbage := filepath.Join(filepath.Dir(absolute), ".skillscli-cleanup-"+strings.TrimPrefix(name, ".skillscli-txn-"))
		if filepath.Dir(garbage) != filepath.Dir(paths.Config) {
			return errors.New("invalid cleanup destination")
		}
		if err := renameAbsent(absolute, garbage); err != nil {
			return err
		}
		absolute = garbage
	}
	// 旧目录可能只读，指纹不含权限。仅终态垃圾可放宽权限以便清理，
	// 活跃事务/唯一恢复备份始终保持原权限。
	if err := filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return errors.New("unsafe object in terminal cleanup directory")
		}
		mode := os.FileMode(0600)
		if entry.IsDir() {
			mode = 0700
		}
		return os.Chmod(path, mode)
	}); err != nil {
		return err
	}
	return os.RemoveAll(absolute)
}
