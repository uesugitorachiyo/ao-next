//go:build windows

package mission

const committedJournalReplacementNeedsDirectorySync = false

func replaceCommittedJournalFile(source, target string) (bool, error) {
	if err := replaceAtomicFile(source, target); err != nil {
		return false, err
	}
	return true, nil
}

func syncCommittedJournalReplacement(_ string) error {
	return nil
}
