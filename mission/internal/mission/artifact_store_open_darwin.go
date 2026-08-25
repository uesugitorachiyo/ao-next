//go:build darwin

package mission

import (
	"syscall"
	"unsafe"
)

const (
	retainedDarwinOpenat   = 463
	retainedDarwinLinkat   = 471
	retainedDarwinUnlinkat = 472
	retainedDarwinMkdirat  = 475
)

func retainedUnixOpenat(dirfd int, path string, flags int, perm uint32) (int, error) {
	pointer, err := syscall.BytePtrFromString(path)
	if err != nil {
		return -1, err
	}
	result, _, errno := syscall.Syscall6(retainedDarwinOpenat, uintptr(dirfd), uintptr(unsafe.Pointer(pointer)), uintptr(flags), uintptr(perm), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(result), nil
}

func retainedUnixMkdirat(dirfd int, path string, perm uint32) error {
	pointer, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(retainedDarwinMkdirat, uintptr(dirfd), uintptr(unsafe.Pointer(pointer)), uintptr(perm), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
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
	_, _, errno := syscall.Syscall6(retainedDarwinLinkat, uintptr(oldDirFD), uintptr(unsafe.Pointer(oldPointer)), uintptr(newDirFD), uintptr(unsafe.Pointer(newPointer)), 0, 0)
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
	_, _, errno := syscall.Syscall6(retainedDarwinUnlinkat, uintptr(dirfd), uintptr(unsafe.Pointer(pointer)), 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
