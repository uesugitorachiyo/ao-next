package mission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

const missionTransactionJournalSchema = "ao.mission.import-transaction-journal.v0.1"

const (
	missionTransactionStatePrepared  = "prepared"
	missionTransactionStateCommitted = "committed"
)

type missionTransactionPaths struct {
	Record        string
	Checkpoint    string
	EventDecision string
	Journal       string
	Lock          string
}

type missionTransactionJournal struct {
	Schema                       string `json:"schema"`
	State                        string `json:"state"`
	MissionID                    string `json:"mission_id"`
	BeforeRecord                 []byte `json:"before_record"`
	BeforeCheckpoint             []byte `json:"before_checkpoint,omitempty"`
	BeforeCheckpointExists       bool   `json:"before_checkpoint_exists"`
	CandidateRecordDigest        string `json:"candidate_record_digest"`
	CandidateCheckpointDigest    string `json:"candidate_checkpoint_digest"`
	EventDecisionTransactional   bool   `json:"event_decision_transactional,omitempty"`
	BeforeEventDecision          []byte `json:"before_event_decision,omitempty"`
	BeforeEventDecisionExists    bool   `json:"before_event_decision_exists"`
	CandidateEventDecisionExists bool   `json:"candidate_event_decision_exists"`
	CandidateEventDecisionDigest string `json:"candidate_event_decision_digest,omitempty"`
}

func (journal *missionTransactionJournal) UnmarshalJSON(body []byte) error {
	type alias missionTransactionJournal
	var decoded alias
	if err := decodeStrictJSONObject(body, &decoded, "Mission transaction journal", map[string]string{
		"schema":                          "string",
		"state":                           "string",
		"mission_id":                      "string",
		"before_record":                   "string",
		"before_checkpoint":               "string",
		"before_checkpoint_exists":        "boolean",
		"candidate_record_digest":         "string",
		"candidate_checkpoint_digest":     "string",
		"event_decision_transactional":    "boolean",
		"before_event_decision":           "string",
		"before_event_decision_exists":    "boolean",
		"candidate_event_decision_exists": "boolean",
		"candidate_event_decision_digest": "string",
	}, []string{
		"schema",
		"state",
		"mission_id",
		"before_record",
		"before_checkpoint_exists",
		"candidate_record_digest",
		"candidate_checkpoint_digest",
	}); err != nil {
		return err
	}
	rawValue, err := decodeExactJSON(body)
	if err != nil {
		return err
	}
	raw, ok := rawValue.(map[string]any)
	if !ok {
		return errors.New("Mission transaction journal must be a JSON object")
	}
	_, checkpointPreimagePresent := raw["before_checkpoint"]
	if checkpointPreimagePresent != decoded.BeforeCheckpointExists {
		return errors.New("Mission transaction journal checkpoint preimage presence does not match before_checkpoint_exists")
	}
	_, eventTransactionalPresent := raw["event_decision_transactional"]
	_, eventPreimagePresent := raw["before_event_decision"]
	_, eventPreimageExistsPresent := raw["before_event_decision_exists"]
	_, candidateEventExistsPresent := raw["candidate_event_decision_exists"]
	_, candidateEventDigestPresent := raw["candidate_event_decision_digest"]
	if decoded.EventDecisionTransactional {
		if !candidateEventExistsPresent && candidateEventDigestPresent {
			decoded.CandidateEventDecisionExists = true
		}
		if !eventTransactionalPresent ||
			!eventPreimageExistsPresent ||
			candidateEventDigestPresent != decoded.CandidateEventDecisionExists ||
			eventPreimagePresent != decoded.BeforeEventDecisionExists {
			return errors.New("Mission transaction journal event decision fields are incomplete")
		}
	} else if eventPreimagePresent ||
		decoded.BeforeEventDecisionExists ||
		decoded.CandidateEventDecisionExists ||
		candidateEventDigestPresent {
		return errors.New("Mission transaction journal has event decision state without a transactional event decision")
	}
	*journal = missionTransactionJournal(decoded)
	return nil
}

func (s Store) transactionPaths(id string) missionTransactionPaths {
	return missionTransactionPaths{
		Record:        s.path(id),
		Checkpoint:    s.checkpointPath(id),
		EventDecision: s.eventLoopPath(id),
		Journal:       s.transactionJournalPath(id),
		Lock:          filepath.Join(s.Root, "missions", id+".transaction.lock"),
	}
}

func (s Store) transactionJournalPath(id string) string {
	return filepath.Join(s.Root, "missions", id+".import-transaction.json")
}

func (s Store) withMissionLock(id string, fn func() error) error {
	return s.withMissionLockMode(id, true, fn)
}

func (s Store) withMissionLockWithoutTempCleanup(id string, fn func() error) error {
	return s.withMissionLockMode(id, false, fn)
}

func (s Store) withMissionLockMode(id string, cleanupTemps bool, fn func() error) (err error) {
	if err := s.Init(); err != nil {
		return err
	}
	paths := s.transactionPaths(id)
	lock, err := os.OpenFile(paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := lock.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if err := lockMissionFile(lock); err != nil {
		return err
	}
	defer func() {
		if unlockErr := unlockMissionFile(lock); err == nil && unlockErr != nil {
			err = unlockErr
		}
	}()
	if cleanupTemps {
		if err := s.cleanupTransactionTempsLocked(paths); err != nil {
			return err
		}
	}
	return fn()
}

func (s Store) cleanupTransactionTempsLocked(paths missionTransactionPaths) error {
	if s.transactionTempCleanup != nil {
		return s.transactionTempCleanup(paths)
	}
	return cleanupMissionTransactionTempsLocked(paths)
}

func (s Store) recoverMissionTransactionLocked(id string) error {
	paths := s.transactionPaths(id)
	body, err := os.ReadFile(paths.Journal)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal missionTransactionJournal
	if err := json.Unmarshal(body, &journal); err != nil {
		return fmt.Errorf("decode Mission transaction journal: %w", err)
	}
	if journal.Schema != missionTransactionJournalSchema ||
		(journal.State != missionTransactionStatePrepared &&
			journal.State != missionTransactionStateCommitted) ||
		journal.MissionID != id ||
		len(journal.BeforeRecord) == 0 ||
		!validSHA256Digest(journal.CandidateRecordDigest) ||
		!validSHA256Digest(journal.CandidateCheckpointDigest) ||
		(journal.BeforeCheckpointExists && len(journal.BeforeCheckpoint) == 0) ||
		(!journal.BeforeCheckpointExists && len(journal.BeforeCheckpoint) != 0) ||
		(journal.EventDecisionTransactional && journal.CandidateEventDecisionExists &&
			!validSHA256Digest(journal.CandidateEventDecisionDigest)) ||
		(journal.EventDecisionTransactional && !journal.CandidateEventDecisionExists &&
			journal.CandidateEventDecisionDigest != "") ||
		(!journal.EventDecisionTransactional &&
			(journal.BeforeEventDecisionExists ||
				len(journal.BeforeEventDecision) != 0 ||
				journal.CandidateEventDecisionExists ||
				journal.CandidateEventDecisionDigest != "")) {
		return errors.New("Mission transaction journal is invalid")
	}
	beforeRecord, err := decodeTransactionRecordPreimage(journal.BeforeRecord, id)
	if err != nil {
		return fmt.Errorf("validate Mission transaction record preimage: %w", err)
	}
	if journal.BeforeCheckpointExists {
		if err := validateTransactionCheckpointPreimage(journal.BeforeCheckpoint, beforeRecord); err != nil {
			return fmt.Errorf("validate Mission transaction checkpoint preimage: %w", err)
		}
	}
	if journal.EventDecisionTransactional && journal.BeforeEventDecisionExists {
		if err := validateTransactionEventDecision(journal.BeforeEventDecision, beforeRecord); err != nil {
			return fmt.Errorf("validate Mission transaction event decision preimage: %w", err)
		}
	}
	currentRecordBody, err := os.ReadFile(paths.Record)
	if err != nil {
		return err
	}
	currentRecord, err := decodeTransactionRecordPreimage(currentRecordBody, id)
	if err != nil {
		return fmt.Errorf("validate current Mission transaction record: %w", err)
	}
	currentDigest := digestBytes(currentRecordBody)
	beforeDigest := digestBytes(journal.BeforeRecord)
	if journal.State == missionTransactionStateCommitted {
		if currentDigest != journal.CandidateRecordDigest {
			return errors.New("committed Mission transaction record digest does not match")
		}
		currentCheckpoint, err := os.ReadFile(paths.Checkpoint)
		if err != nil {
			return err
		}
		if err := validateTransactionCheckpointPreimage(currentCheckpoint, currentRecord); err != nil {
			return fmt.Errorf("validate committed Mission transaction checkpoint: %w", err)
		}
		if digestBytes(currentCheckpoint) != journal.CandidateCheckpointDigest {
			return errors.New("committed Mission transaction checkpoint digest does not match")
		}
		if journal.EventDecisionTransactional {
			if journal.CandidateEventDecisionExists {
				currentEventDecision, err := os.ReadFile(paths.EventDecision)
				if err != nil {
					return err
				}
				if err := validateTransactionEventDecision(currentEventDecision, currentRecord); err != nil {
					return fmt.Errorf("validate committed Mission transaction event decision: %w", err)
				}
				if digestBytes(currentEventDecision) != journal.CandidateEventDecisionDigest {
					return errors.New("committed Mission transaction event decision digest does not match")
				}
			} else if _, err := os.Stat(paths.EventDecision); !os.IsNotExist(err) {
				return errors.New("committed Mission transaction retained an unexpected event decision")
			}
		}
		s.cleanupCommittedJournal(paths)
		return nil
	}
	if currentDigest != beforeDigest && currentDigest != journal.CandidateRecordDigest {
		return errors.New("Mission transaction recovery compare-and-swap conflict")
	}
	if err := s.runTransactionFault("before_recovery_restore", paths); err != nil {
		return err
	}
	if err := writeAtomicFile(paths.Record, journal.BeforeRecord, 0o644); err != nil {
		return fmt.Errorf("restore Mission transaction record: %w", err)
	}
	if journal.BeforeCheckpointExists {
		if err := writeAtomicFile(paths.Checkpoint, journal.BeforeCheckpoint, 0o644); err != nil {
			return fmt.Errorf("restore Mission transaction checkpoint: %w", err)
		}
	} else if err := removeFileAndSync(paths.Checkpoint); err != nil {
		return fmt.Errorf("remove Mission transaction checkpoint: %w", err)
	}
	if journal.EventDecisionTransactional {
		if journal.BeforeEventDecisionExists {
			if err := writeAtomicFile(paths.EventDecision, journal.BeforeEventDecision, 0o644); err != nil {
				return fmt.Errorf("restore Mission transaction event decision: %w", err)
			}
		} else if err := removeFileAndSync(paths.EventDecision); err != nil {
			return fmt.Errorf("remove Mission transaction event decision: %w", err)
		}
	}
	if err := removeFileAndSync(paths.Journal); err != nil {
		return fmt.Errorf("remove recovered Mission transaction journal: %w", err)
	}
	return nil
}

func (s Store) updateWithCheckpointTransaction(
	id string,
	mutate func(*Record) error,
) (Record, error) {
	return s.updateMissionTransactionWithTimestamp(id, true, true, func(record *Record) (*EventLoopDecision, error) {
		if err := mutate(record); err != nil {
			return nil, err
		}
		return eventDecisionForRecord(*record), nil
	})
}

func (s Store) updateWithCheckpointAndEventDecisionTransaction(
	id string,
	mutate func(*Record) (*EventLoopDecision, error),
) (Record, error) {
	return s.updateMissionTransaction(id, mutate)
}

func (s Store) updateMissionTransaction(
	id string,
	mutate func(*Record) (*EventLoopDecision, error),
) (Record, error) {
	return s.updateMissionTransactionWithTimestamp(id, true, false, mutate)
}

func (s Store) updateMissionTransactionWithTimestamp(
	id string,
	touchUpdatedAt bool,
	forceEventDecisionTransaction bool,
	mutate func(*Record) (*EventLoopDecision, error),
) (Record, error) {
	var result Record
	err := s.withMissionLock(id, func() error {
		if err := s.recoverMissionTransactionLocked(id); err != nil {
			return err
		}
		paths := s.transactionPaths(id)
		beforeRecord, err := os.ReadFile(paths.Record)
		if err != nil {
			return err
		}
		if err := decodeRecordBytes(beforeRecord, &result); err != nil {
			return err
		}
		eventDecision, err := mutate(&result)
		if err != nil {
			return err
		}
		if touchUpdatedAt {
			result.UpdatedAtUTC = now(s.Clock)
		}
		if err := validateRecordWorkflowContract(result); err != nil {
			return err
		}
		candidateRecord, err := marshalIndentedLine(result)
		if err != nil {
			return err
		}
		candidateCheckpoint, err := marshalIndentedLine(BuildCheckpointBundle(result))
		if err != nil {
			return err
		}
		beforeCheckpoint, checkpointErr := os.ReadFile(paths.Checkpoint)
		checkpointExists := checkpointErr == nil
		if checkpointErr != nil && !os.IsNotExist(checkpointErr) {
			return checkpointErr
		}
		var candidateEventDecision []byte
		var beforeEventDecision []byte
		eventDecisionExists := false
		eventDecisionTransactional := forceEventDecisionTransaction || eventDecision != nil
		if eventDecisionTransactional {
			if eventDecision != nil {
				if err := validateEventDecisionForRecord(*eventDecision, result); err != nil {
					return err
				}
				candidateEventDecision, err = marshalIndentedLine(eventDecision)
				if err != nil {
					return err
				}
			}
			beforeEventDecision, err = os.ReadFile(paths.EventDecision)
			eventDecisionExists = err == nil
			if err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if err := s.runTransactionFault("before_record_cas", paths); err != nil {
			return err
		}
		currentRecord, err := os.ReadFile(paths.Record)
		if err != nil {
			return err
		}
		if !bytes.Equal(currentRecord, beforeRecord) {
			return errors.New("Mission import compare-and-swap conflict")
		}
		journal := missionTransactionJournal{
			Schema:                     missionTransactionJournalSchema,
			State:                      missionTransactionStatePrepared,
			MissionID:                  id,
			BeforeRecord:               beforeRecord,
			BeforeCheckpoint:           beforeCheckpoint,
			BeforeCheckpointExists:     checkpointExists,
			CandidateRecordDigest:      digestBytes(candidateRecord),
			CandidateCheckpointDigest:  digestBytes(candidateCheckpoint),
			EventDecisionTransactional: eventDecisionTransactional,
			BeforeEventDecision:        beforeEventDecision,
			BeforeEventDecisionExists:  eventDecisionExists,
		}
		if eventDecision != nil {
			journal.CandidateEventDecisionExists = true
			journal.CandidateEventDecisionDigest = digestBytes(candidateEventDecision)
		}
		journalBody, err := marshalIndentedLine(journal)
		if err != nil {
			return err
		}
		if err := writeAtomicFile(paths.Journal, journalBody, 0o600); err != nil {
			return fmt.Errorf("persist Mission transaction journal: %w", err)
		}
		if err := writeAtomicFile(paths.Record, candidateRecord, 0o644); err != nil {
			return fmt.Errorf("persist Mission transaction record: %w", err)
		}
		if err := s.runTransactionFault("before_checkpoint_replace", paths); err != nil {
			return err
		}
		if err := writeAtomicFile(paths.Checkpoint, candidateCheckpoint, 0o644); err != nil {
			return fmt.Errorf("persist Mission transaction checkpoint: %w", err)
		}
		if eventDecisionTransactional {
			if err := s.runTransactionFault("before_event_decision_replace", paths); err != nil {
				return err
			}
			if eventDecision != nil {
				if err := writeAtomicFile(paths.EventDecision, candidateEventDecision, 0o644); err != nil {
					return fmt.Errorf("persist Mission transaction event decision: %w", err)
				}
			} else if err := removeFileAndSync(paths.EventDecision); err != nil {
				return fmt.Errorf("remove Mission transaction event decision: %w", err)
			}
		}
		if err := s.runTransactionFault("before_journal_commit", paths); err != nil {
			return err
		}
		journal.State = missionTransactionStateCommitted
		journalBody, err = marshalIndentedLine(journal)
		if err != nil {
			return err
		}
		committed, cleanupAllowed, err := s.writeCommittedTransactionJournal(paths, journalBody)
		if err != nil {
			return fmt.Errorf("commit Mission transaction journal: %w", err)
		}
		if !committed {
			return errors.New("Mission transaction journal did not reach its commit point")
		}
		if cleanupAllowed {
			s.cleanupCommittedJournal(paths)
		}
		return nil
	})
	return result, err
}

func eventDecisionForRecord(record Record) *EventLoopDecision {
	if len(record.Steps) == 0 {
		return nil
	}
	step := record.Steps[len(record.Steps)-1]
	return &EventLoopDecision{
		Schema:              EventLoopDecisionSchema,
		MissionID:           record.MissionID,
		CorrelationID:       record.CorrelationID,
		Iteration:           step.Iteration,
		Status:              step.Result,
		Route:               step.Route,
		ExactNextAction:     step.ExactNextAction,
		ExecutesWork:        false,
		ApprovesWork:        false,
		MutatesRepositories: false,
		GeneratedAtUTC:      step.GeneratedAtUTC,
	}
}

func (s Store) writeCommittedTransactionJournal(
	paths missionTransactionPaths,
	body []byte,
) (committed bool, cleanupAllowed bool, err error) {
	dir := filepath.Dir(paths.Journal)
	file, err := os.CreateTemp(dir, "."+filepath.Base(paths.Journal)+".tmp-*")
	if err != nil {
		return false, false, err
	}
	tempPath := file.Name()
	defer func() {
		file.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return false, false, err
	}
	if _, err := file.Write(body); err != nil {
		return false, false, err
	}
	if err := file.Sync(); err != nil {
		return false, false, err
	}
	if err := file.Close(); err != nil {
		return false, false, err
	}
	replaced, err := replaceCommittedJournalFile(tempPath, paths.Journal)
	if err != nil {
		if replaced {
			return true, false, nil
		}
		return false, false, err
	}
	committed = true
	if committedJournalReplacementNeedsDirectorySync {
		if err := s.runTransactionFault("after_committed_journal_replace", paths); err != nil {
			return true, false, nil
		}
	}
	if err := syncCommittedJournalReplacement(dir); err != nil {
		return true, false, nil
	}
	return true, true, nil
}

func (s Store) cleanupCommittedJournal(paths missionTransactionPaths) {
	if err := s.runTransactionFault("committed_journal_cleanup", paths); err != nil {
		return
	}
	_ = removeFileAndSync(paths.Journal)
}

func (s Store) runTransactionFault(stage string, paths missionTransactionPaths) error {
	if s.transactionFault == nil {
		return nil
	}
	return s.transactionFault(stage, paths)
}

func cleanupMissionTransactionTempsLocked(paths missionTransactionPaths) error {
	dir := filepath.Dir(paths.Record)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	prefixes := []string{
		"." + filepath.Base(paths.Record) + ".tmp-",
		"." + filepath.Base(paths.Checkpoint) + ".tmp-",
		"." + filepath.Base(paths.EventDecision) + ".tmp-",
		"." + filepath.Base(paths.Journal) + ".tmp-",
	}
	removed := false
	for _, entry := range entries {
		for _, prefix := range prefixes {
			if entry.Type().IsRegular() && bytes.HasPrefix([]byte(entry.Name()), []byte(prefix)) {
				if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
					return err
				}
				removed = true
				break
			}
		}
	}
	if removed {
		return syncDirectory(dir)
	}
	return nil
}

func decodeTransactionRecordPreimage(body []byte, id string) (Record, error) {
	var record Record
	if err := decodeRecordBytes(body, &record); err != nil {
		return Record{}, err
	}
	if record.Schema != RecordSchema || record.MissionID != id {
		return Record{}, errors.New("Mission transaction record preimage identity does not match")
	}
	if err := validateTransactionPreimageRoundTrip(body, record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func validateTransactionCheckpointPreimage(body []byte, record Record) error {
	var checkpoint MissionCheckpointBundle
	if err := json.Unmarshal(body, &checkpoint); err != nil {
		return err
	}
	if checkpoint.Schema != CheckpointBundleSchema ||
		checkpoint.MissionID != record.MissionID ||
		checkpoint.CorrelationID != record.CorrelationID ||
		checkpoint.Status != "ready" ||
		checkpoint.CheckpointCount != len(record.Checkpoints) ||
		checkpoint.SafeToExecute ||
		checkpoint.ExecutesWork ||
		checkpoint.ApprovesWork ||
		checkpoint.MutatesRepositories {
		return errors.New("Mission transaction checkpoint preimage is invalid")
	}
	if checkpoint.CheckpointCount == 0 {
		if checkpoint.LatestCheckpoint != nil {
			return errors.New("Mission transaction checkpoint preimage has a latest checkpoint at zero count")
		}
	} else {
		if checkpoint.LatestCheckpoint == nil {
			return errors.New("Mission transaction checkpoint preimage is missing its latest checkpoint")
		}
		expected := record.Checkpoints[len(record.Checkpoints)-1]
		if !reflect.DeepEqual(*checkpoint.LatestCheckpoint, expected) {
			return errors.New("Mission transaction checkpoint preimage disagrees with its paired Mission record")
		}
	}
	expected := BuildCheckpointBundle(record)
	expectedBody, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(expectedBody, &expected); err != nil {
		return err
	}
	expected.GeneratedAtUTC = checkpoint.GeneratedAtUTC
	if expected.ReturnGate != nil && checkpoint.ReturnGate != nil {
		expected.ReturnGate.GeneratedAtUTC = checkpoint.ReturnGate.GeneratedAtUTC
	}
	gotBody, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	wantBody, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(gotBody, wantBody) {
		return errors.New("Mission transaction checkpoint preimage semantics disagree with its paired Mission record")
	}
	return validateTransactionPreimageRoundTrip(body, checkpoint)
}

func validateTransactionEventDecision(body []byte, record Record) error {
	var decision EventLoopDecision
	if err := json.Unmarshal(body, &decision); err != nil {
		return err
	}
	if err := validateEventDecisionForRecord(decision, record); err != nil {
		return err
	}
	return validateTransactionPreimageRoundTrip(body, decision)
}

func validateEventDecisionForRecord(decision EventLoopDecision, record Record) error {
	if len(record.Steps) == 0 {
		return errors.New("Mission transaction event decision has no paired continuation step")
	}
	step := record.Steps[len(record.Steps)-1]
	if decision.Schema != EventLoopDecisionSchema ||
		decision.MissionID != record.MissionID ||
		decision.CorrelationID != record.CorrelationID ||
		decision.Iteration != step.Iteration ||
		decision.Status != step.Result ||
		decision.Route != step.Route ||
		decision.ExactNextAction != step.ExactNextAction ||
		decision.GeneratedAtUTC != step.GeneratedAtUTC ||
		decision.ExecutesWork ||
		decision.ApprovesWork ||
		decision.MutatesRepositories {
		return errors.New("Mission transaction event decision disagrees with its paired Mission record")
	}
	return nil
}

func validateTransactionPreimageRoundTrip(body []byte, value any) error {
	decoded, err := decodeExactJSON(body)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	roundTripped, err := decodeExactJSON(encoded)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(decoded, roundTripped) {
		return errors.New("Mission transaction preimage is not strict")
	}
	return nil
}

func marshalIndentedLine(value any) ([]byte, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func writeAtomicFile(path string, body []byte, mode os.FileMode) error {
	_, err := writeAtomicFileWithReplacementState(path, body, mode)
	return err
}

func writeAtomicFileWithReplacementState(
	path string,
	body []byte,
	mode os.FileMode,
) (replaced bool, err error) {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, err
	}
	tempPath := file.Name()
	defer func() {
		file.Close()
		if !replaced {
			_ = os.Remove(tempPath)
		}
	}()
	if err = file.Chmod(mode); err != nil {
		return false, err
	}
	if _, err = file.Write(body); err != nil {
		return false, err
	}
	if err = file.Sync(); err != nil {
		return false, err
	}
	if err = file.Close(); err != nil {
		return false, err
	}
	if err = replaceAtomicFile(tempPath, path); err != nil {
		return false, err
	}
	replaced = true
	if err = syncDirectory(dir); err != nil {
		return true, err
	}
	return true, nil
}

func removeFileAndSync(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
