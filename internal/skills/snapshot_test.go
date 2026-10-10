package skills

import (
	"testing"

	"github.com/zz-zhi54/skillscli/internal/config"
)

func TestDiscoverSnapshotMissingScopesStillChecksSafetyAndDuplicates(t *testing.T) {
	root := t.TempDir()
	skillFixture(t, root, "skills/alpha")
	skillFixture(t, root, "extra/alpha")
	for _, paths := range [][]string{{"absent", "skills"}, {"absent/subdirectory"}} {
		got, err := DiscoverSnapshot(root, config.Source{Paths: paths})
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if len(paths) == 2 {
			want = 1
		}
		if len(got) != want {
			t.Fatalf("snapshot = %#v, expected %d Skills", got, want)
		}
	}
	// 缺失目录不能掩盖非法路径或多个存在路径间的重名。
	for _, paths := range [][]string{{"absent/../outside"}, {"absent", "../outside"}, {"absent", "skills", "extra"}} {
		got, err := DiscoverSnapshot(root, config.Source{Paths: paths})
		if err == nil || got != nil {
			t.Fatalf("unsafe/duplicate snapshot accepted: %#v, %v", got, err)
		}
	}
	// 严格的原入口不改变行为。
	if _, err := Discover(root, config.Source{Paths: []string{"absent"}}); err == nil {
		t.Fatal("Discover stopped rejecting missing paths")
	}
}
