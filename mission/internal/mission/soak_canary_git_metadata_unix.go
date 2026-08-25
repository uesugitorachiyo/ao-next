//go:build !windows

package mission

import (
	"errors"
	"os"
	"syscall"
)

func openSoakCanaryGitMetadataRegularNoFollow(path string) (*os.File, error) {
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
		_ = syscall.Close(descriptor)
		return nil, errors.New("open soak canary Git metadata")
	}
	return file, nil
}

func validateSoakCanaryGitMetadataPlatformComponent(string) error {
	return nil
}
