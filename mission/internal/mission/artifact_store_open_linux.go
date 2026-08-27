//go:build linux

package mission

import (
	"syscall"
	"unsafe"
)

func retainedUnixOpenat(dirfd int, path string, flags int, perm uint32) (int, error) {
	return syscall.Openat(dirfd, path, flags, perm)
}

func retainedUnixMkdirat(dirfd int, path string, perm uint32) error {
	return syscall.Mkdirat(dirfd, path, perm)
}

func retainedUnixLinkat(oldDirFD int, oldPath string, newDirFD int, newPath string) error {
	oldPointer, err := syscall.BytePtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPointer, err := syscall.BytePtrFromString(newPath)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(
		syscall.SYS_LINKAT,
		uintptr(oldDirFD),
		uintptr(unsafe.Pointer(oldPointer)),
		uintptr(newDirFD),
		uintptr(unsafe.Pointer(newPointer)),
		0,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func retainedUnixUnlinkat(dirfd int, path string) error {
	pointer, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_UNLINKAT, uintptr(dirfd), uintptr(unsafe.Pointer(pointer)), 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
