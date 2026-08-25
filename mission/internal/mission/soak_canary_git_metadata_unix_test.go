//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSoakCanaryGitVerifierRejectsFIFOMetadataWithoutBlocking(t *testing.T) {
	tests := []struct {
		name string
		path func(string, string) string
	}{
		{
			name: "index FIFO",
			path: func(root, _ string) string {
				return filepath.Join(root, ".git", "index")
			},
		},
		{
			name: "loose object FIFO",
			path: func(root, head string) string {
				return filepath.Join(root, ".git", "objects", head[:2], head[2:])
			},
		},
		{
			name: "packed object FIFO",
			path: func(root, _ string) string {
				runSoakCanaryTestGit(t, root, "gc", "--prune=now")
				packs, err := filepath.Glob(
					filepath.Join(root, ".git", "objects", "pack", "*.pack"),
				)
				if err != nil || len(packs) != 1 {
					t.Fatalf("locate packed Git object: packs=%v err=%v", packs, err)
				}
				return packs[0]
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			head := initializeSoakCanaryGitRepository(t, root)
			path := test.path(root, head)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			err := (InProcessSoakCanaryGitVerifier{}).Verify(root, head)
			if err == nil || (!strings.Contains(strings.ToLower(err.Error()), "regular") &&
				!strings.Contains(strings.ToLower(err.Error()), "unsafe")) {
				t.Fatalf("FIFO metadata accepted or wrong error: %v", err)
			}
		})
	}
}
