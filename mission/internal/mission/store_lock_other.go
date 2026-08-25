//go:build !darwin && !linux && !windows

package mission

import (
	"errors"
	"os"
)

func lockMissionFile(_ *os.File) error {
	return errors.New("Mission locking is unsupported on this platform")
}

func unlockMissionFile(_ *os.File) error {
	return errors.New("Mission locking is unsupported on this platform")
}
