package workspacev2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceRepositoryBoundaryHasNoConcreteEngineSurface(
	t *testing.T,
) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"Kopia" + "Repository",
		"Open" + "Kopia",
		"Create" + "KopiaFilesystem",
		"Password" + "FormatBackup",
		"kopia" + ".repository",
		"kopia" + ".blobcfg",
		`"kopia` + `"`,
		"kopia" + "-v3",
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() ||
			!strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		for _, token := range forbidden {
			if strings.Contains(string(raw), token) {
				t.Errorf("%s directly references %q", name, token)
			}
		}
	}
}
