package mission

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func isUnavailableWindowsSymlinkPrivilege(goos string, err error) bool {
	return goos == "windows" && errors.Is(err, syscall.Errno(1314))
}

func createTestSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if isUnavailableWindowsSymlinkPrivilege(runtime.GOOS, err) {
			t.Skipf("Windows symlink privilege is unavailable: %v", err)
		}
		t.Fatalf("create test symlink: %v", err)
	}
}

func requireTestSymlinkCapability(t *testing.T) {
	t.Helper()
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "symlink-capability-probe")
	createTestSymlink(t, target, link)
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove test symlink capability probe: %v", err)
	}
}

func TestUnavailableWindowsSymlinkPrivilegeClassification(t *testing.T) {
	if !isUnavailableWindowsSymlinkPrivilege("windows", syscall.Errno(1314)) {
		t.Fatal("Windows ERROR_PRIVILEGE_NOT_HELD was not classified")
	}
	if isUnavailableWindowsSymlinkPrivilege("linux", syscall.Errno(1314)) ||
		isUnavailableWindowsSymlinkPrivilege("windows", os.ErrNotExist) {
		t.Fatal("unrelated symlink error was classified as unavailable privilege")
	}
}
