//go:build windows

package mission

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
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

func TestAONextJournalRequestReaderPreventsParentReplacement(t *testing.T) {
	directory := t.TempDir()
	realDirectory := filepath.Join(directory, "real")
	if err := os.Mkdir(realDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(realDirectory, "prefix.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(directory, "moved")
	originalHook := beforeAONextJournalWindowsComponentOpen
	defer func() { beforeAONextJournalWindowsComponentOpen = originalHook }()
	attempted := false
	beforeAONextJournalWindowsComponentOpen = func(parent, child string) error {
		if filepath.Clean(parent) != filepath.Clean(realDirectory) || filepath.Base(child) != "prefix.json" {
			return nil
		}
		attempted = true
		if err := os.Rename(realDirectory, moved); err == nil {
			if restoreErr := os.Rename(moved, realDirectory); restoreErr != nil {
				return fmt.Errorf("parent replacement succeeded and restore failed: %v", restoreErr)
			}
			return fmt.Errorf("parent replacement succeeded")
		}
		return nil
	}
	if _, _, err := readBoundedAbsoluteRegularFileOutsideRoot(
		path, filepath.Join(directory, "excluded"), 1024,
	); err != nil {
		t.Fatalf("handle-retained read failed: %v", err)
	}
	if !attempted {
		t.Fatal("replacement probe did not reach the parent-to-child boundary")
	}
}

func TestAONextJournalRequestReaderRejectsShortPathAliasInsideRoot(t *testing.T) {
	excludedRoot := filepath.Join(t.TempDir(), "Mission State With Long Name")
	if err := os.Mkdir(excludedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(excludedRoot, "prefix.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := windowsShortPath(t, path)
	if strings.EqualFold(alias, path) {
		t.Skip("8.3 short names are unavailable on this volume")
	}
	if _, _, err := readBoundedAbsoluteRegularFileOutsideRoot(alias, excludedRoot, 1024); err == nil {
		t.Fatal("8.3 alias inside Mission root unexpectedly accepted")
	}
}

func TestWindowsPathWithinIsCaseInsensitive(t *testing.T) {
	if !windowsPathWithin(`C:\Mission State`, `c:\mission state\prefix.json`) {
		t.Fatal("case-only alias escaped Mission root")
	}
}

func windowsShortPath(t *testing.T, path string) string {
	t.Helper()
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetShortPathNameW")
	buffer := make([]uint16, syscall.MAX_PATH)
	length, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(pointer)),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	if length == 0 {
		t.Skipf("GetShortPathNameW unavailable: %v", callErr)
	}
	if length >= uintptr(len(buffer)) {
		t.Skip("short path exceeds fixed test buffer")
	}
	return syscall.UTF16ToString(buffer[:length])
}
