//go:build !windows

package mission

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func readBoundedRegularFile(path string, limit int64) ([]byte, error) {
	descriptor, err := syscall.Open(
		path,
		syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		syscall.Close(descriptor)
		return nil, fmt.Errorf("open issue repair request")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("issue repair request must be a regular file")
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
	if strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return "", nil, fmt.Errorf("AO Next journal locator must be a clean absolute path")
	}
	excludedAbsolute, err := filepath.Abs(excludedRoot)
	if err != nil {
		return "", nil, err
	}
	if pathWithin(filepath.Clean(excludedAbsolute), path) {
		return "", nil, fmt.Errorf("AO Next journal locator must be outside the Mission state root")
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	root, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", nil, err
	}
	current := root
	for index, part := range parts {
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
		if index < len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		} else {
			flags |= syscall.O_NONBLOCK
		}
		next, openErr := retainedUnixOpenat(current, part, flags, 0)
		_ = syscall.Close(current)
		if openErr != nil {
			return "", nil, openErr
		}
		current = next
	}
	file := os.NewFile(uintptr(current), path)
	if file == nil {
		_ = syscall.Close(current)
		return "", nil, fmt.Errorf("open AO Next journal prefix")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("AO Next journal prefix must be a regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", nil, err
	}
	if int64(len(body)) > limit {
		return "", nil, fmt.Errorf("AO Next journal prefix exceeds %d bytes", limit)
	}
	after, err := file.Stat()
	if err != nil {
		return "", nil, err
	}
	if !os.SameFile(info, after) || info.Size() != after.Size() {
		return "", nil, fmt.Errorf("AO Next journal prefix identity changed while reading")
	}
	return path, body, nil
}
