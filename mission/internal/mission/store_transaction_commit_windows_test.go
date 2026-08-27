//go:build windows

package mission

import "testing"

func TestCommittedJournalWindowsReplacementPolicyIsWriteThrough(t *testing.T) {
	if committedJournalReplacementNeedsDirectorySync {
		t.Fatal("Windows committed journal replacement must not rely on directory sync")
	}
	if missionMoveFileWriteThrough != 0x00000008 {
		t.Fatalf("Windows committed journal replacement lost MoveFileExW write-through: %#x", missionMoveFileWriteThrough)
	}
}
