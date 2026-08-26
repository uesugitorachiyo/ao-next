//go:build windows

package mission

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAONextJournalRequestReaderRejectsReparseAncestor(t *testing.T) {
	directory := t.TempDir()
	realDirectory := filepath.Join(directory, "real")
	if err := os.Mkdir(realDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDirectory, "prefix.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedDirectory := filepath.Join(directory, "linked")
	if err := os.Symlink(realDirectory, linkedDirectory); err != nil {
		t.Skipf("Windows symlink privilege unavailable: %v", err)
	}
	if _, _, err := readBoundedAbsoluteRegularFileOutsideRoot(
		filepath.Join(linkedDirectory, "prefix.json"), filepath.Join(directory, "excluded"), 1024,
	); err == nil {
		t.Fatal("reparse ancestor unexpectedly accepted")
	}
}
