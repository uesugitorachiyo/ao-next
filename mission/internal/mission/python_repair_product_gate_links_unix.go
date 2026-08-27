//go:build !windows

package mission

import (
	"os"
	"syscall"
)

func pythonRepairGateHasMultipleLinks(_ *os.File, info os.FileInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink != 1, nil
}
