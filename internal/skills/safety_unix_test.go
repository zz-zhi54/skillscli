//go:build darwin || linux

package skills

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/zz-zhi54/skillscli/internal/config"
)

func TestSpecialFileSafety(t *testing.T) {
	for _, kind := range []string{"SKILL.md", "nested file", "discovery entry"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dir := skillFixture(t, root, "skills/idea")
			file := filepath.Join(dir, "pipe")
			switch kind {
			case "SKILL.md":
				dir = filepath.Join(root, "skills", "fifo-skill")
				// 创建独立目录，不先生成普通 SKILL.md。
				writeFile(t, filepath.Join(dir, "README.md"), "test")
				file = filepath.Join(dir, "SKILL.md")
			case "discovery entry":
				file = filepath.Join(root, "skills", "pipe")
			}
			if err := syscall.Mkfifo(file, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Discover(root, config.Source{})
			requireError(t, err, filepath.Base(file))
			if kind != "discovery entry" {
				_, err = Hash(dir)
				requireError(t, err, "special files", filepath.Base(file))
				_, err = Validate(dir)
				requireError(t, err, "special files", filepath.Base(file))
			}
		})
	}
}
