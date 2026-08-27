package art

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// updateGoldens regenerates golden files when tests run with -update,
// mirroring the repository's established golden pattern.
var updateGoldens = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGoldens {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("cannot create testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("cannot write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s missing (run with -update): %v", path, err)
	}
	if string(want) != got {
		t.Fatalf("golden mismatch for %s:\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}
