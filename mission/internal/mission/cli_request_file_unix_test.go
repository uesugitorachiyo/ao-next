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

func TestAONextJournalRequestReaderRejectsSymlinkedAncestorAndFIFO(t *testing.T) {
	directory := t.TempDir()
	realDirectory := filepath.Join(directory, "real")
	if err := os.Mkdir(realDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(realDirectory, "prefix.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedDirectory := filepath.Join(directory, "linked")
	if err := os.Symlink(realDirectory, linkedDirectory); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readBoundedAbsoluteRegularFileOutsideRoot(
		filepath.Join(linkedDirectory, "prefix.json"), filepath.Join(directory, "excluded"), 1024,
	); err == nil {
		t.Fatal("symlinked ancestor unexpectedly accepted")
	}

	fifo := filepath.Join(directory, "prefix.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readBoundedAbsoluteRegularFileOutsideRoot(
		fifo, filepath.Join(directory, "excluded"), 1024,
	); err == nil {
		t.Fatal("FIFO unexpectedly accepted")
	}
}
