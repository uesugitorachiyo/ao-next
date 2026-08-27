//go:build darwin || linux

package mission

import "os"

const committedJournalReplacementNeedsDirectorySync = true

func replaceCommittedJournalFile(source, target string) (bool, error) {
	if err := os.Rename(source, target); err != nil {
		return false, err
	}
	return true, nil
}

func syncCommittedJournalReplacement(path string) error {
	return syncDirectory(path)
}
