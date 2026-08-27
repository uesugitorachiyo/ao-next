//go:build windows

package mission

import (
	"fmt"
	"os"
	"syscall"
)

const (
	correlationWindowsSecurityAnonymous         = 0x00000000
	correlationWindowsSecurityImpersonationMask = 0x00030000
	correlationWindowsSecuritySQOSPresent       = 0x00100000
)

func correlationWindowsOpenFlags() uint32 {
	return syscall.FILE_ATTRIBUTE_NORMAL |
		syscall.FILE_FLAG_OPEN_REPARSE_POINT |
		correlationWindowsSecuritySQOSPresent |
		correlationWindowsSecurityAnonymous
}

func openCorrelationInput(path string) (*os.File, error) {
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		pathPointer,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		correlationWindowsOpenFlags(),
		0,
	)
	if err != nil {
		return nil, err
	}
	fileType, err := syscall.GetFileType(handle)
	if err != nil {
		_ = syscall.CloseHandle(handle)
		return nil, err
	}
	if !correlationWindowsFileTypeAllowed(fileType) {
		_ = syscall.CloseHandle(handle)
		return nil, fmt.Errorf("correlation input is not a disk file: file type %#x", fileType)
	}
	return os.NewFile(uintptr(handle), path), nil
}
