//go:build windows

package mission

import (
	"os"
	"syscall"
	"unsafe"
)

const missionLockExclusive = 0x00000002

var (
	kernel32MissionLock = syscall.NewLazyDLL("kernel32.dll")
	lockFileExProc      = kernel32MissionLock.NewProc("LockFileEx")
	unlockFileExProc    = kernel32MissionLock.NewProc("UnlockFileEx")
)

func lockMissionFile(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := lockFileExProc.Call(
		file.Fd(),
		missionLockExclusive,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result == 0 {
		return callErr
	}
	return nil
}

func unlockMissionFile(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := unlockFileExProc.Call(
		file.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result == 0 {
		return callErr
	}
	return nil
}
