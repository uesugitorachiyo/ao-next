//go:build !darwin && !linux && !windows

package mission

import "os"

func replaceAtomicFile(source, destination string) error {
	return os.Rename(source, destination)
}

func syncDirectory(_ string) error {
	return nil
}
