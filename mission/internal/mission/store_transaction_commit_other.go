//go:build !darwin && !linux && !windows

package mission

import "os"

const committedJournalReplacementNeedsDirectorySync = false

func replaceCommittedJournalFile(source, target string) (bool, error) {
	if err := os.Rename(source, target); err != nil {
		return false, err
	}
	return true, nil
}

func syncCommittedJournalReplacement(_ string) error {
	return nil
}
