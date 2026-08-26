//go:build windows

package mission

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const aoNextJournalWindowsBackupSemantics = 0x0200_0000

func readBoundedRegularFile(path string, limit int64) ([]byte, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !pathInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("issue repair request must be a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(pathInfo, info) {
		return nil, fmt.Errorf("issue repair request identity changed before reading")
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("issue repair request exceeds %d bytes", limit)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, after) {
		return nil, fmt.Errorf("issue repair request identity changed while reading")
	}
	return body, nil
}

func readBoundedAbsoluteRegularFileOutsideRoot(path, excludedRoot string, limit int64) (string, []byte, error) {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(path)
	if strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || clean != path || volume == "" ||
		strings.HasPrefix(path, `\\`) || strings.HasPrefix(volume, `\\`) {
		return "", nil, fmt.Errorf("AO Next journal locator must be a clean absolute local drive path")
	}
	excludedAbsolute, err := filepath.Abs(excludedRoot)
	if err != nil {
		return "", nil, err
	}
	if windowsPathWithin(filepath.Clean(excludedAbsolute), path) {
		return "", nil, fmt.Errorf("AO Next journal locator must be outside the Mission state root")
	}
	remainder := strings.TrimLeft(path[len(volume):], `\/`)
	if remainder == "" {
		return "", nil, fmt.Errorf("AO Next journal locator must name a file")
	}
	parts := strings.FieldsFunc(remainder, func(r rune) bool { return r == '\\' || r == '/' })
	current := volume + string(filepath.Separator)
	var leaf *os.File
	var before syscall.ByHandleFileInformation
	for index, part := range parts {
		current = filepath.Join(current, part)
		pointer, err := syscall.UTF16PtrFromString(current)
		if err != nil {
			return "", nil, err
		}
		handle, err := syscall.CreateFile(
			pointer,
			syscall.GENERIC_READ,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
			nil,
			syscall.OPEN_EXISTING,
			syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT|aoNextJournalWindowsBackupSemantics,
			0,
		)
		if err != nil {
			return "", nil, err
		}
		var information syscall.ByHandleFileInformation
		if err := syscall.GetFileInformationByHandle(handle, &information); err != nil {
			_ = syscall.CloseHandle(handle)
			return "", nil, err
		}
		last := index == len(parts)-1
		if information.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
			(!last && information.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0) ||
			(last && information.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0) {
			_ = syscall.CloseHandle(handle)
			return "", nil, fmt.Errorf("AO Next journal locator contains a reparse or non-regular component")
		}
		if !last {
			_ = syscall.CloseHandle(handle)
			continue
		}
		leaf = os.NewFile(uintptr(handle), path)
		if leaf == nil {
			_ = syscall.CloseHandle(handle)
			return "", nil, fmt.Errorf("open AO Next journal prefix")
		}
		before = information
	}
	defer leaf.Close()
	info, err := leaf.Stat()
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("AO Next journal prefix must be a regular file")
	}
	body, err := io.ReadAll(io.LimitReader(leaf, limit+1))
	if err != nil {
		return "", nil, err
	}
	if int64(len(body)) > limit {
		return "", nil, fmt.Errorf("AO Next journal prefix exceeds %d bytes", limit)
	}
	var after syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(leaf.Fd()), &after); err != nil {
		return "", nil, err
	}
	if before.VolumeSerialNumber != after.VolumeSerialNumber || before.FileIndexHigh != after.FileIndexHigh ||
		before.FileIndexLow != after.FileIndexLow || before.FileSizeHigh != after.FileSizeHigh || before.FileSizeLow != after.FileSizeLow {
		return "", nil, fmt.Errorf("AO Next journal prefix identity changed while reading")
	}
	return path, body, nil
}

func windowsPathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
