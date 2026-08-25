//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestIssueRepairRequestReaderRejectsSymlinkAndFIFO(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "request.json")
	if err := os.WriteFile(regular, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "request-link.json")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRegularFile(symlink, issueRepairRequestLimit); err == nil {
		t.Fatal("symlink request unexpectedly accepted")
	}

	fifo := filepath.Join(dir, "request.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRegularFile(fifo, issueRepairRequestLimit); err == nil {
		t.Fatal("FIFO request unexpectedly accepted")
	}
}
