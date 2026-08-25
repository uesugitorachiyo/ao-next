//go:build windows

package mission

import (
	"errors"
	"os"
	"syscall"
)

func openSoakCanaryGitMetadataRegularNoFollow(path string) (*os.File, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		pointer,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	var information syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &information); err != nil {
		syscall.CloseHandle(handle)
		return nil, err
	}
	if information.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		information.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 {
		syscall.CloseHandle(handle)
		return nil, errors.New("soak canary Git metadata path is a reparse or non-file entry")
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		syscall.CloseHandle(handle)
		return nil, errors.New("open soak canary Git metadata")
	}
	return file, nil
}

func validateSoakCanaryGitMetadataPlatformComponent(path string) error {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := syscall.GetFileAttributes(pointer)
	if err != nil {
		return err
	}
	if attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("soak canary Git metadata path contains a reparse point")
	}
	return nil
}
