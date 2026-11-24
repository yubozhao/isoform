package destinations

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestYansuBackendRemoved(t *testing.T) {
	if _, ok := Lookup("yansu_backend"); ok {
		t.Error("retired Yansu destination still listed")
	}
	for _, root := range moduleRoots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() && skipDirs[entry.Name()] {
				return filepath.SkipDir
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			code := string(src)
			// The generic Git URL binding rejects this domain before its preflight dial.
			if filepath.Clean(path) == filepath.Clean(filepath.Join(moduleRoots[0], "clone.go")) {
				code = strings.Replace(code, "\nconst retiredGitDomain = \"yansu.ai\"\n", "\n", 1)
			}
			if strings.Contains(strings.ToLower(code), "yansu.ai") {
				t.Errorf("%s references a retired Yansu host", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
