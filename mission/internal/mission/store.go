package mission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	Root                   string
	Clock                  func() time.Time
	transactionFault       func(string, missionTransactionPaths) error
	transactionTempCleanup func(missionTransactionPaths) error
	missionRecordLoad      func(string, func() error) error
}

const missionRecordLoadWorkerLimit = 8

func DefaultRoot() string {
	if v := os.Getenv("AO_MISSION_HOME"); strings.TrimSpace(v) != "" {
		return v
	}
	return ".ao-mission"
}
func NewStore(root string) Store {
	if root == "" {
		root = DefaultRoot()
	}
	return Store{Root: root}
}
func (s Store) Init() error           { return os.MkdirAll(filepath.Join(s.Root, "missions"), 0o755) }
func (s Store) path(id string) string { return filepath.Join(s.Root, "missions", id+".json") }
func (s Store) eventLoopPath(id string) string {
	return filepath.Join(s.Root, "missions", id+".event-loop-decision.json")
}
func (s Store) checkpointPath(id string) string {
	return filepath.Join(s.Root, "missions", id+".checkpoint-resume-bundle.json")
}

func DigestObjective(objective string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(objective)))
	return "sha256:" + hex.EncodeToString(sum[:])
}
func MissionID(objective string, t time.Time) string {
	sum := sha256.Sum256([]byte(t.UTC().Format(time.RFC3339Nano) + "\x00" + objective))
	return "mission-" + hex.EncodeToString(sum[:])[:16]
}

func (s Store) Start(objective string) (Record, error) {
	if strings.TrimSpace(objective) == "" {
		return Record{}, errors.New("objective is required")
	}
	if err := s.Init(); err != nil {
		return Record{}, err
	}
	t := time.Now
	if s.Clock != nil {
		t = s.Clock
	}
	stamp := now(t)
	id := MissionID(objective, t())
	route := DecideRoute(id, objective, nil)
	rec := Record{Schema: RecordSchema, MissionID: id, Objective: objective, ObjectiveDigest: DigestObjective(objective), Status: "active", CreatedAtUTC: stamp, UpdatedAtUTC: stamp, CurrentRoute: route.Route, CurrentPhase: "routing", ExactNextAction: route.ExactNextAction, ArtifactRefs: []ArtifactRef{}, Blockers: []string{}, Steps: []ContinuationStep{}}
	AppendRouteHistory(&rec, route)
	return rec, s.Save(rec)
}

type ObjectiveStartOptions struct {
	CorrelationID string
}

var correlationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func (s Store) StartObjective(objective string, opts ObjectiveStartOptions) (ObjectiveWorkflowContract, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return ObjectiveWorkflowContract{}, errors.New("objective is required")
	}
	if err := s.Init(); err != nil {
		return ObjectiveWorkflowContract{}, err
	}
	t := time.Now
	if s.Clock != nil {
		t = s.Clock
	}
	stamp := now(t)
	id := MissionID(objective, t())
	correlationID := strings.TrimSpace(opts.CorrelationID)
	if correlationID == "" {
		correlationID = "corr-" + strings.TrimPrefix(id, "mission-")
	}
	if !correlationIDPattern.MatchString(correlationID) {
		return ObjectiveWorkflowContract{}, errors.New("correlation ID must match [A-Za-z0-9][A-Za-z0-9._:-]{0,127}")
	}
	contract := DecideObjectiveWorkflow(id, correlationID, objective, stamp)
	rec := Record{
		Schema:           RecordSchema,
		MissionID:        id,
		CorrelationID:    correlationID,
		Objective:        objective,
		ObjectiveDigest:  contract.ObjectiveDigest,
		Status:           "active",
		CreatedAtUTC:     stamp,
		UpdatedAtUTC:     stamp,
		CurrentRoute:     contract.InitialRoute,
		CurrentPhase:     "routing",
		ExactNextAction:  contract.ExactNextAction,
		ArtifactRefs:     []ArtifactRef{},
		Blockers:         []string{},
		Steps:            []ContinuationStep{},
		WorkflowContract: &contract,
	}
	AppendRouteHistory(&rec, RouteDecision{
		Schema:          RouteSchema,
		MissionID:       id,
		Route:           contract.InitialRoute,
		Reason:          "objective workflow classified as " + contract.RoutingClass,
		SafeToRequest:   true,
		SafeToExecute:   false,
		SafeToPromote:   false,
		ExactNextAction: contract.ExactNextAction,
		GeneratedAtUTC:  stamp,
	})
	if err := s.Save(rec); err != nil {
		return ObjectiveWorkflowContract{}, err
	}
	return contract, nil
}

func (s Store) Load(id string) (Record, error) {
	var r Record
	err := s.withMissionLock(id, func() error {
		if err := s.recoverMissionTransactionLocked(id); err != nil {
			return err
		}
		body, err := os.ReadFile(s.path(id))
		if err != nil {
			return err
		}
		return decodeRecordBytes(body, &r)
	})
	return r, err
}

type ListFilters struct {
	Status string
	Route  string
}

type storeListStats struct {
	StoreListCount int
	StoreFileReads int
}

func (s Store) List() ([]Record, error) {
	return s.ListFiltered(ListFilters{})
}

func (s Store) ListFiltered(filters ListFilters) ([]Record, error) {
	records, _, err := s.listFilteredWithStats(filters)
	return records, err
}

func (s Store) listFilteredWithStats(filters ListFilters) ([]Record, storeListStats, error) {
	stats := storeListStats{StoreListCount: 1}
	if err := s.Init(); err != nil {
		return nil, stats, err
	}
	entries, err := os.ReadDir(filepath.Join(s.Root, "missions"))
	if err != nil {
		return nil, stats, err
	}
	type candidate struct {
		id   string
		path string
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isMissionRecordCandidateName(entry.Name()) {
			continue
		}
		candidates = append(candidates, candidate{
			id:   strings.TrimSuffix(entry.Name(), ".json"),
			path: filepath.Join(s.Root, "missions", entry.Name()),
		})
	}
	type loadResult struct {
		record   Record
		isRecord bool
		read     bool
		err      error
	}
	results := make([]loadResult, len(candidates))
	jobs := make(chan int)
	workerCount := min(max(runtime.GOMAXPROCS(0), 2), missionRecordLoadWorkerLimit, len(candidates))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				candidate := candidates[index]
				var result loadResult
				result.err = s.runMissionRecordLoad(candidate.id, func() error {
					return s.withMissionLockWithoutTempCleanup(candidate.id, func() error {
						if err := s.recoverMissionTransactionLocked(candidate.id); err != nil {
							return err
						}
						body, err := os.ReadFile(candidate.path)
						if err != nil {
							return err
						}
						result.read = true
						var envelope struct {
							Schema string `json:"schema"`
						}
						if err := json.Unmarshal(body, &envelope); err != nil {
							return err
						}
						if envelope.Schema != RecordSchema {
							return nil
						}
						if err := decodeRecordBytes(body, &result.record); err != nil {
							return err
						}
						if result.record.MissionID != candidate.id {
							return fmt.Errorf("Mission record filename does not match mission_id")
						}
						result.isRecord = true
						return nil
					})
				})
				results[index] = result
			}
		}()
	}
	for index := range candidates {
		jobs <- index
	}
	close(jobs)
	workers.Wait()

	records := make([]Record, 0, len(results))
	for _, result := range results {
		if result.read {
			stats.StoreFileReads++
		}
		if result.err != nil {
			return nil, stats, result.err
		}
		if !result.isRecord {
			continue
		}
		rec := result.record
		if filters.Status != "" && rec.Status != filters.Status {
			continue
		}
		if filters.Route != "" && rec.CurrentRoute != filters.Route {
			continue
		}
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAtUTC == records[j].CreatedAtUTC {
			return records[i].MissionID < records[j].MissionID
		}
		return records[i].CreatedAtUTC < records[j].CreatedAtUTC
	})
	return records, stats, nil
}

func (s Store) runMissionRecordLoad(id string, load func() error) error {
	if s.missionRecordLoad != nil {
		return s.missionRecordLoad(id, load)
	}
	return load()
}

func isMissionRecordCandidateName(name string) bool {
	if filepath.Ext(name) != ".json" {
		return false
	}
	for _, suffix := range []string{
		".event-loop-decision.json",
		".checkpoint-resume-bundle.json",
		".import-transaction.json",
	} {
		if strings.HasSuffix(name, suffix) {
			return false
		}
	}
	return true
}
func (s Store) Save(r Record) error {
	if err := validateRecordWorkflowContract(r); err != nil {
		return err
	}
	if _, err := os.Stat(s.path(r.MissionID)); err == nil {
		_, err = s.updateMissionTransactionWithTimestamp(
			r.MissionID,
			false,
			true,
			func(current *Record) (*EventLoopDecision, error) {
				*current = r
				return eventDecisionForRecord(r), nil
			},
		)
		return err
	} else if !os.IsNotExist(err) {
		return err
	}
	body, err := marshalIndentedLine(r)
	if err != nil {
		return err
	}
	return s.withMissionLock(r.MissionID, func() error {
		if err := s.recoverMissionTransactionLocked(r.MissionID); err != nil {
			return err
		}
		if _, err := os.Stat(s.path(r.MissionID)); err == nil {
			return errors.New("Mission record appeared during save; retry the operation")
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := removeFileAndSync(s.checkpointPath(r.MissionID)); err != nil {
			return err
		}
		if err := removeFileAndSync(s.eventLoopPath(r.MissionID)); err != nil {
			return err
		}
		checkpointBody, err := marshalIndentedLine(BuildCheckpointBundle(r))
		if err != nil {
			return err
		}
		cleanupSidecars := func() {
			_ = removeFileAndSync(s.checkpointPath(r.MissionID))
			_ = removeFileAndSync(s.eventLoopPath(r.MissionID))
		}
		checkpointReplaced, checkpointErr := s.writeInitialMissionFile(
			r.MissionID,
			"after_initial_checkpoint_replace",
			s.checkpointPath(r.MissionID),
			checkpointBody,
			0o644,
		)
		if checkpointErr != nil && !checkpointReplaced {
			cleanupSidecars()
			return checkpointErr
		}
		if decision := eventDecisionForRecord(r); decision != nil {
			eventBody, err := marshalIndentedLine(decision)
			if err != nil {
				cleanupSidecars()
				return err
			}
			eventReplaced, eventErr := s.writeInitialMissionFile(
				r.MissionID,
				"after_initial_event_decision_replace",
				s.eventLoopPath(r.MissionID),
				eventBody,
				0o644,
			)
			if eventErr != nil && !eventReplaced {
				cleanupSidecars()
				return eventErr
			}
		}
		recordReplaced, recordErr := s.writeInitialMissionFile(
			r.MissionID,
			"after_initial_record_replace",
			s.path(r.MissionID),
			body,
			0o644,
		)
		if recordErr != nil && !recordReplaced {
			cleanupSidecars()
			return recordErr
		}
		return nil
	})
}

func (s Store) writeInitialMissionFile(
	missionID string,
	faultStage string,
	path string,
	body []byte,
	mode os.FileMode,
) (bool, error) {
	replaced, err := writeAtomicFileWithReplacementState(path, body, mode)
	if err != nil {
		return replaced, err
	}
	if err := s.runTransactionFault(faultStage, s.transactionPaths(missionID)); err != nil {
		return true, err
	}
	return true, nil
}

func (s Store) SaveEventLoopDecision(decision EventLoopDecision) error {
	body, err := marshalIndentedLine(decision)
	if err != nil {
		return err
	}
	return s.withMissionLock(decision.MissionID, func() error {
		if err := s.recoverMissionTransactionLocked(decision.MissionID); err != nil {
			return err
		}
		recordBody, err := os.ReadFile(s.path(decision.MissionID))
		if err != nil {
			return err
		}
		var record Record
		if err := decodeRecordBytes(recordBody, &record); err != nil {
			return err
		}
		if err := validateTransactionEventDecision(body, record); err != nil {
			return err
		}
		return writeAtomicFile(s.eventLoopPath(decision.MissionID), body, 0o644)
	})
}

func (s Store) LoadEventLoopDecision(id string) (EventLoopDecision, error) {
	var decision EventLoopDecision
	err := s.withMissionLock(id, func() error {
		if err := s.recoverMissionTransactionLocked(id); err != nil {
			return err
		}
		recordBody, err := os.ReadFile(s.path(id))
		if err != nil {
			return err
		}
		var record Record
		if err := decodeRecordBytes(recordBody, &record); err != nil {
			return err
		}
		body, err := os.ReadFile(s.eventLoopPath(id))
		if err != nil {
			return err
		}
		if err := validateTransactionEventDecision(body, record); err != nil {
			return err
		}
		return json.Unmarshal(body, &decision)
	})
	return decision, err
}

func (s Store) SaveCheckpointBundle(bundle MissionCheckpointBundle) error {
	body, err := marshalIndentedLine(bundle)
	if err != nil {
		return err
	}
	return s.withMissionLock(bundle.MissionID, func() error {
		if err := s.recoverMissionTransactionLocked(bundle.MissionID); err != nil {
			return err
		}
		recordBody, err := os.ReadFile(s.path(bundle.MissionID))
		if err != nil {
			return err
		}
		var record Record
		if err := decodeRecordBytes(recordBody, &record); err != nil {
			return err
		}
		if err := validateTransactionCheckpointPreimage(body, record); err != nil {
			return err
		}
		return writeAtomicFile(s.checkpointPath(bundle.MissionID), body, 0o644)
	})
}

func (s Store) LoadCheckpointBundle(id string) (MissionCheckpointBundle, error) {
	var bundle MissionCheckpointBundle
	err := s.withMissionLock(id, func() error {
		if err := s.recoverMissionTransactionLocked(id); err != nil {
			return err
		}
		recordBody, err := os.ReadFile(s.path(id))
		if err != nil {
			return err
		}
		var record Record
		if err := decodeRecordBytes(recordBody, &record); err != nil {
			return err
		}
		body, err := os.ReadFile(s.checkpointPath(id))
		if err != nil {
			return err
		}
		if err := validateTransactionCheckpointPreimage(body, record); err != nil {
			return err
		}
		return json.Unmarshal(body, &bundle)
	})
	return bundle, err
}
func (s Store) Update(id string, fn func(*Record) error) (Record, error) {
	return s.updateWithCheckpointTransaction(id, fn)
}

func decodeRecordBytes(body []byte, record *Record) error {
	if err := json.Unmarshal(body, record); err != nil {
		return err
	}
	return validateRecordWorkflowContract(*record)
}

func ValidatePublicSafeText(text string) error {
	localPath := `/` + `Users/`
	privateKey := `BEGIN (RSA |OPENSSH |PRIVATE )?PRIVATE ` + `KEY`
	openAIKey := `sk-` + `[A-Za-z0-9]{20,}`
	gitHubToken := `gh[pousr]_` + `[A-Za-z0-9]{20,}`
	secretAssignment := `(?i)(password|` + `token|cookie)\s*[:=]\s*[^\s]+`
	patterns := []*regexp.Regexp{regexp.MustCompile(localPath), regexp.MustCompile(privateKey), regexp.MustCompile(openAIKey), regexp.MustCompile(gitHubToken), regexp.MustCompile(secretAssignment)}
	for _, p := range patterns {
		if p.MatchString(text) {
			return fmt.Errorf("unsafe public content detected")
		}
	}
	return nil
}
