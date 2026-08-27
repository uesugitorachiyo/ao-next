package mission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	TerminalIndexContract      = "ao.canonical-terminal-index.v1"
	TerminalIndexImportSchema  = "ao.mission.terminal-index-import.v1"
	terminalIndexMaxFileBytes  = 1 << 20
	terminalIndexMaxTotalBytes = 16 << 20
	terminalIndexMaxArtifacts  = 128
	fresh60CompletedNextAction = "Fresh 60-node Mission-to-Atlas soak complete; no further execution is authorized."
)

type TerminalIndexArtifact struct {
	Role     string `json:"role"`
	Sequence int    `json:"sequence"`
	Path     string `json:"path"`
	Schema   string `json:"schema"`
	SHA256   string `json:"sha256"`
	State    string `json:"state"`
}

type TerminalIndexLineage struct {
	FromSequence int    `json:"from_sequence"`
	ToSequence   int    `json:"to_sequence"`
	Relation     string `json:"relation"`
}

type TerminalIndexCounts struct {
	Total     int `json:"total"`
	Minimum   int `json:"minimum"`
	Completed int `json:"completed"`
	Ready     int `json:"ready"`
	Blocked   int `json:"blocked"`
	Failed    int `json:"failed"`
}

type TerminalIndexLease struct {
	MinimumMinutes int    `json:"minimum_minutes"`
	TargetMinutes  int    `json:"target_minutes"`
	MaximumMinutes int    `json:"maximum_minutes"`
	ElapsedMinutes int    `json:"elapsed_minutes"`
	Status         string `json:"status"`
}

type TerminalIndexSafety struct {
	ExecutesWork        bool `json:"executes_work"`
	ApprovesWork        bool `json:"approves_work"`
	MutatesRepositories bool `json:"mutates_repositories"`
	CallsProviders      bool `json:"calls_providers"`
	Publishes           bool `json:"publishes"`
	Releases            bool `json:"releases"`
	Deploys             bool `json:"deploys"`
	AdvancesAuthority   bool `json:"advances_authority"`
}

type CanonicalTerminalIndex struct {
	ContractVersion            string                  `json:"contract_version"`
	SchemaDigest               string                  `json:"schema_digest"`
	MissionID                  string                  `json:"mission_id"`
	EvidenceRoot               string                  `json:"evidence_root"`
	GeneratedAtUTC             string                  `json:"generated_at_utc"`
	Artifacts                  []TerminalIndexArtifact `json:"artifacts"`
	Lineage                    []TerminalIndexLineage  `json:"lineage"`
	TerminalReference          string                  `json:"terminal_reference"`
	Counts                     TerminalIndexCounts     `json:"counts"`
	Lease                      TerminalIndexLease      `json:"lease"`
	CompletionObserved         bool                    `json:"completion_observed"`
	CanonicalEvidenceAgreement bool                    `json:"canonical_evidence_agreement"`
	ReadinessPassed            bool                    `json:"readiness_passed"`
	ReturnGateStatus           string                  `json:"return_gate_status"`
	FinalResponseAllowed       bool                    `json:"final_response_allowed"`
	ConflictCodes              []string                `json:"conflict_codes"`
	ConflictSummaries          []string                `json:"conflict_summaries"`
	ExactNextAction            string                  `json:"exact_next_action"`
	SafetyBoundaries           TerminalIndexSafety     `json:"safety_boundaries"`
	Digest                     string                  `json:"digest"`
}

type TerminalIndexImportReadback struct {
	Schema                     string              `json:"schema"`
	Surface                    string              `json:"surface"`
	MissionID                  string              `json:"mission_id"`
	IndexDigest                string              `json:"index_digest"`
	StateDigest                string              `json:"state_digest"`
	GeneratedAtUTC             string              `json:"generated_at_utc"`
	Status                     string              `json:"status"`
	TerminalProjectionStatus   string              `json:"terminal_projection_status,omitempty"`
	TerminalProjectionReadOnly bool                `json:"terminal_projection_read_only,omitempty"`
	Counts                     TerminalIndexCounts `json:"counts"`
	Lease                      TerminalIndexLease  `json:"lease"`
	CompletionObserved         bool                `json:"completion_observed"`
	TimingCompliant            bool                `json:"timing_compliant"`
	CanonicalEvidenceAgreement bool                `json:"canonical_evidence_agreement"`
	ReadinessPassed            bool                `json:"readiness_passed"`
	ReturnGateStatus           string              `json:"return_gate_status"`
	FinalResponseAllowed       bool                `json:"final_response_allowed"`
	ConflictCodes              []string            `json:"conflict_codes"`
	ExactNextAction            string              `json:"exact_next_action"`
	ReadOnly                   bool                `json:"read_only"`
	SafeToExecute              bool                `json:"safe_to_execute"`
	ExecutesWork               bool                `json:"executes_work"`
	ApprovesWork               bool                `json:"approves_work"`
	MutatesRepositories        bool                `json:"mutates_repositories"`
	CallsProviders             bool                `json:"calls_providers"`
	Publishes                  bool                `json:"publishes"`
	Releases                   bool                `json:"releases"`
	Deploys                    bool                `json:"deploys"`
	AdvancesAuthority          bool                `json:"advances_authority"`
}

type terminalIndexObservation struct {
	missionID         string
	schema            string
	completed         int
	ready             int
	blocked           int
	failed            int
	minimumNodes      int
	minimumMinutes    int
	minimumMinutesSet bool
	targetMinutes     int
	maximumMinutes    int
	elapsedMinutes    int
	leaseStatus       string
	final             bool
	nextAction        string
	safety            TerminalIndexSafety
}

func ImportTerminalIndex(root, indexPath, statePath string) (TerminalIndexImportReadback, error) {
	index, err := loadCanonicalTerminalIndex(indexPath)
	if err != nil {
		return TerminalIndexImportReadback{}, err
	}
	if err := VerifyTerminalIndex(root, index); err != nil {
		return TerminalIndexImportReadback{}, err
	}
	if existing, err := LoadTerminalIndexImport(statePath); err == nil {
		if existing.IndexDigest != index.Digest {
			return TerminalIndexImportReadback{}, errors.New("conflicting terminal index import")
		}
		return existing, nil
	} else if !os.IsNotExist(err) {
		return TerminalIndexImportReadback{}, err
	}
	readback := TerminalIndexImportReadback{
		Schema:                     TerminalIndexImportSchema,
		Surface:                    "import",
		MissionID:                  index.MissionID,
		IndexDigest:                index.Digest,
		GeneratedAtUTC:             index.GeneratedAtUTC,
		Status:                     terminalImportStatus(index),
		TerminalProjectionStatus:   terminalOperatorStatus(index.FinalResponseAllowed, index.Counts),
		TerminalProjectionReadOnly: true,
		Counts:                     index.Counts,
		Lease:                      index.Lease,
		CompletionObserved:         index.CompletionObserved,
		TimingCompliant:            index.Lease.Status == "within_window",
		CanonicalEvidenceAgreement: index.CanonicalEvidenceAgreement,
		ReadinessPassed:            index.ReadinessPassed,
		ReturnGateStatus:           index.ReturnGateStatus,
		FinalResponseAllowed:       index.FinalResponseAllowed,
		ConflictCodes:              append([]string{}, index.ConflictCodes...),
		ExactNextAction:            index.ExactNextAction,
		ReadOnly:                   true,
	}
	signTerminalIndexImport(&readback)
	body, err := json.MarshalIndent(readback, "", "  ")
	if err != nil {
		return TerminalIndexImportReadback{}, err
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return TerminalIndexImportReadback{}, err
	}
	if err := writeAtomicFile(statePath, append(body, '\n'), 0o644); err != nil {
		return TerminalIndexImportReadback{}, err
	}
	return readback, nil
}

func LoadTerminalIndexImport(path string) (TerminalIndexImportReadback, error) {
	var readback TerminalIndexImportReadback
	body, err := readTerminalRegularFile(path)
	if err != nil {
		return readback, err
	}
	if err := decodeTerminalJSON(body, &readback); err != nil {
		return readback, err
	}
	if readback.Schema != TerminalIndexImportSchema || readback.IndexDigest == "" || !readback.ReadOnly {
		return readback, errors.New("terminal index import state is invalid")
	}
	unsigned := readback
	unsigned.StateDigest = ""
	body, err = json.Marshal(unsigned)
	if err != nil {
		return readback, err
	}
	if readback.StateDigest != digestBytes(body) {
		return readback, errors.New("terminal index import state digest mismatch")
	}
	if readback.TerminalProjectionStatus == "" && !readback.TerminalProjectionReadOnly {
		readback.TerminalProjectionStatus = terminalOperatorStatus(readback.FinalResponseAllowed, readback.Counts)
		readback.TerminalProjectionReadOnly = true
		signTerminalIndexImport(&readback)
	}
	if err := validateTerminalIndexImportReadback(readback); err != nil {
		return readback, err
	}
	return readback, nil
}

func projectRecordWithTerminalState(record Record, statePath string) (Record, error) {
	if strings.TrimSpace(statePath) == "" {
		return record, nil
	}
	readback, err := LoadTerminalIndexImport(statePath)
	if err != nil {
		return Record{}, err
	}
	if readback.Surface != "import" || readback.MissionID != record.MissionID {
		return Record{}, errors.New("terminal index import state mission identity mismatch")
	}
	recordTime, err := time.Parse(time.RFC3339, record.UpdatedAtUTC)
	if err != nil {
		return Record{}, errors.New("Mission update timestamp is invalid")
	}
	terminalTime, _ := time.Parse(time.RFC3339, readback.GeneratedAtUTC)
	if terminalTime.Before(recordTime) {
		return Record{}, errors.New("terminal index import state is stale")
	}
	sourceStatus := record.Status
	terminalStatus := terminalOperatorStatus(readback.FinalResponseAllowed, readback.Counts)
	effectiveStatus := terminalStatus
	if terminalStatus == "active" && sourceStatus != "active" {
		effectiveStatus = sourceStatus
	}
	record.SourceRecordStatus = sourceStatus
	record.TerminalProjectionStatus = terminalStatus
	record.TerminalProjectionReadOnly = true
	record.EffectiveOperatorStatus = effectiveStatus
	record.CurrentPhase = readback.Status
	record.ExactNextAction = readback.ExactNextAction
	record.Evidence.AtlasWorkgraph = &NodeCounts{
		Total: readback.Counts.Total, Ready: readback.Counts.Ready,
		Blocked: readback.Counts.Blocked, Completed: readback.Counts.Completed,
		Failed: readback.Counts.Failed,
	}
	record.Evidence.AtlasRecommendation = &AtlasRecommendationReadbackCounts{
		Status: readback.Status, TotalNodes: readback.Counts.Total,
		CompletedNodes: readback.Counts.Completed, ReadyNodes: readback.Counts.Ready,
		CheckpointCount: len(record.Checkpoints), ElapsedMinutes: readback.Lease.ElapsedMinutes,
		MinMinutesMet: readback.TimingCompliant, LeaseTimeStatus: readback.Lease.Status,
		ReturnGateStatus:     readback.ReturnGateStatus,
		FinalResponseAllowed: readback.FinalResponseAllowed,
		ExactNextAction:      readback.ExactNextAction,
	}
	if record.GoalLease != nil {
		lease := *record.GoalLease
		lease.MinNodes = readback.Counts.Minimum
		lease.MinMinutes = readback.Lease.MinimumMinutes
		lease.MaxMinutes = readback.Lease.MaximumMinutes
		lease.UpdatedAtUTC = readback.GeneratedAtUTC
		record.GoalLease = &lease
	}
	record.Status = effectiveStatus
	record.ReturnGate = &ReturnGate{
		Schema: ReturnGateSchema, MissionID: record.MissionID,
		Status: readback.ReturnGateStatus, FinalResponseAllowed: readback.FinalResponseAllowed,
		Reason: "validated canonical terminal index", CompletedNodes: readback.Counts.Completed,
		MinNodes: readback.Counts.Minimum, ReadyNodesRemaining: readback.Counts.Ready,
		HardBlocker:     readback.Counts.Blocked > 0 || readback.Counts.Failed > 0,
		ExactNextAction: readback.ExactNextAction, GeneratedAtUTC: readback.GeneratedAtUTC,
	}
	reconciliation := RouteReconciliation{
		Schema: "ao.mission.route-reconciliation.v0.3", MissionID: record.MissionID,
		CorrelationID: record.CorrelationID, CurrentRoute: record.CurrentRoute,
	}
	if record.Reconciliation != nil {
		reconciliation = *record.Reconciliation
	}
	reconciliation.Status = readback.Status
	reconciliation.AtlasReadyNodes = readback.Counts.Ready
	reconciliation.ExactNextAction = readback.ExactNextAction
	reconciliation.GeneratedAtUTC = readback.GeneratedAtUTC
	record.Reconciliation = &reconciliation
	return record, nil
}

func terminalOperatorStatus(finalResponseAllowed bool, counts TerminalIndexCounts) string {
	if finalResponseAllowed {
		return "done"
	}
	if counts.Blocked > 0 || counts.Failed > 0 {
		return "blocked"
	}
	return "active"
}

func validateTerminalIndexImportReadback(readback TerminalIndexImportReadback) error {
	if strings.TrimSpace(readback.MissionID) == "" || !validSHA256Digest(readback.IndexDigest) ||
		!validSHA256Digest(readback.StateDigest) {
		return errors.New("terminal index import state identity is invalid")
	}
	if readback.Status != "reconciled" && readback.Status != "reconciled_fail_closed" &&
		readback.Status != "no_canonical_terminal" {
		return errors.New("terminal index import state status is invalid")
	}
	if readback.TerminalProjectionStatus != terminalOperatorStatus(readback.FinalResponseAllowed, readback.Counts) ||
		!readback.TerminalProjectionReadOnly {
		return errors.New("terminal index import state projection is invalid")
	}
	if _, err := time.Parse(time.RFC3339, readback.GeneratedAtUTC); err != nil {
		return errors.New("terminal index import state timestamp is invalid")
	}
	if !readback.ReadOnly || readback.SafeToExecute || readback.ExecutesWork ||
		readback.ApprovesWork || readback.MutatesRepositories || readback.CallsProviders ||
		readback.Publishes || readback.Releases || readback.Deploys || readback.AdvancesAuthority {
		return errors.New("terminal index import state safety boundary is invalid")
	}
	counts := readback.Counts
	if counts.Total < 0 || counts.Minimum < 0 || counts.Completed < 0 || counts.Ready < 0 ||
		counts.Blocked < 0 || counts.Failed < 0 || counts.Minimum > counts.Total ||
		counts.Completed+counts.Ready+counts.Blocked+counts.Failed != counts.Total {
		return errors.New("terminal index import state counts are contradictory")
	}
	lease := readback.Lease
	if lease.MinimumMinutes < 0 || lease.TargetMinutes < lease.MinimumMinutes ||
		lease.MaximumMinutes < lease.TargetMinutes || lease.ElapsedMinutes < 0 ||
		(lease.Status != "within_window" && lease.Status != "minimum_not_met" && lease.Status != "maximum_exceeded") {
		return errors.New("terminal index import state lease is contradictory")
	}
	if readback.FinalResponseAllowed && (!readback.ReadinessPassed ||
		readback.ReturnGateStatus != "final_response_allowed" || !readback.CompletionObserved ||
		!readback.TimingCompliant || !readback.CanonicalEvidenceAgreement ||
		counts.Completed != counts.Total || counts.Ready != 0 || counts.Blocked != 0 || counts.Failed != 0 ||
		len(readback.ConflictCodes) != 0) {
		return errors.New("terminal index import state return gate is contradictory")
	}
	return nil
}

// ValidateTerminalSurfaceAgreement proves that Mission's four exported views
// carry one canonical authority payload while retaining surface-specific state
// digests. Equal index digests alone never excuse a payload mismatch.
func ValidateTerminalSurfaceAgreement(readbacks []TerminalIndexImportReadback) error {
	if len(readbacks) != 4 {
		return errors.New("terminal surface agreement requires exactly four views")
	}
	expectedSurfaces := map[string]bool{
		"inspect": false, "checkpoint": false, "event-index": false, "command-readback": false,
	}
	stateDigests := map[string]bool{}
	var indexDigest string
	var canonicalBody []byte
	for _, readback := range readbacks {
		if _, exists := expectedSurfaces[readback.Surface]; !exists || expectedSurfaces[readback.Surface] {
			return fmt.Errorf("terminal surface %q is missing or duplicated", readback.Surface)
		}
		expectedSurfaces[readback.Surface] = true
		unsigned := readback
		unsigned.StateDigest = ""
		body, err := json.Marshal(unsigned)
		if err != nil {
			return err
		}
		if readback.StateDigest == "" || readback.StateDigest != digestBytes(body) {
			return fmt.Errorf("terminal surface %q state digest mismatch", readback.Surface)
		}
		if stateDigests[readback.StateDigest] {
			return errors.New("terminal state digests must be distinct across surfaces")
		}
		stateDigests[readback.StateDigest] = true
		if indexDigest == "" {
			if readback.IndexDigest == "" {
				return errors.New("terminal index digest is required across surfaces")
			}
			indexDigest = readback.IndexDigest
		} else if readback.IndexDigest != indexDigest {
			return errors.New("terminal index digest mismatch across surfaces")
		}
		canonical := readback
		canonical.Surface = ""
		canonical.StateDigest = ""
		body, err = json.Marshal(canonical)
		if err != nil {
			return err
		}
		if canonicalBody == nil {
			canonicalBody = body
		} else if !bytes.Equal(canonicalBody, body) {
			return errors.New("terminal canonical payload mismatch across surfaces")
		}
	}
	return nil
}

func VerifyTerminalIndex(root string, index CanonicalTerminalIndex) error {
	if index.ContractVersion != TerminalIndexContract {
		return fmt.Errorf("terminal index contract_version must be %s", TerminalIndexContract)
	}
	if index.SchemaDigest != digestBytes([]byte(TerminalIndexContract)) {
		return errors.New("terminal index schema digest mismatch")
	}
	if index.MissionID == "" || index.EvidenceRoot != "." {
		return errors.New("terminal index identity is invalid")
	}
	if _, err := time.Parse(time.RFC3339, index.GeneratedAtUTC); err != nil {
		return errors.New("terminal index generated_at_utc is invalid")
	}
	unsigned := index
	unsigned.Digest = ""
	body, err := json.Marshal(unsigned)
	if err != nil {
		return err
	}
	if index.Digest != digestBytes(body) {
		return errors.New("terminal index digest mismatch")
	}
	if len(index.Artifacts) == 0 || len(index.Artifacts) > terminalIndexMaxArtifacts {
		return errors.New("terminal index artifact count is outside limits")
	}
	if terminalIndexSafetyEnabled(index.SafetyBoundaries) {
		return errors.New("terminal index safety boundary must remain false")
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	observations := map[string]terminalIndexObservation{}
	seenRoles := map[string]bool{}
	totalBytes := 0
	previousSequence := -1
	for position, artifact := range index.Artifacts {
		if artifact.Sequence <= previousSequence || seenRoles[artifact.Role] {
			return errors.New("terminal index artifact lineage is non-monotonic")
		}
		previousSequence = artifact.Sequence
		seenRoles[artifact.Role] = true
		if !validTerminalArtifactState(artifact.Role, artifact.State) {
			return fmt.Errorf("%s artifact state is invalid", artifact.Role)
		}
		if position > 0 {
			if position-1 >= len(index.Lineage) {
				return errors.New("terminal index explicit lineage is incomplete")
			}
			edge := index.Lineage[position-1]
			prior := index.Artifacts[position-1]
			if edge.FromSequence != prior.Sequence || edge.ToSequence != artifact.Sequence || edge.Relation != "precedes" {
				return errors.New("terminal index explicit lineage is invalid")
			}
		}
		path, err := terminalContainedPath(rootAbs, artifact.Path)
		if err != nil {
			return err
		}
		artifactBody, err := readTerminalRegularFile(path)
		if err != nil {
			return fmt.Errorf("%s artifact: %w", artifact.Role, err)
		}
		totalBytes += len(artifactBody)
		if totalBytes > terminalIndexMaxTotalBytes {
			return errors.New("terminal index input exceeds total size limit")
		}
		if digestBytes(artifactBody) != artifact.SHA256 {
			return fmt.Errorf("%s artifact digest mismatch", artifact.Role)
		}
		var raw map[string]any
		if err := decodeTerminalJSON(artifactBody, &raw); err != nil {
			return fmt.Errorf("%s artifact: %w", artifact.Role, err)
		}
		observation := observeTerminalIndexArtifact(raw)
		if artifact.Schema == "" || observation.schema != artifact.Schema {
			return fmt.Errorf("%s artifact schema mismatch", artifact.Role)
		}
		if observation.missionID != "" && observation.missionID != index.MissionID {
			return fmt.Errorf("%s artifact mission identity mismatch", artifact.Role)
		}
		if terminalIndexSafetyEnabled(observation.safety) {
			return fmt.Errorf("%s artifact safety boundary must remain false", artifact.Role)
		}
		observations[artifact.Role] = observation
	}
	if len(index.Lineage) != len(index.Artifacts)-1 {
		return errors.New("terminal index explicit lineage length is invalid")
	}
	rootObservation, hasRoot := observations["root"]
	if !hasRoot {
		return errors.New("terminal index root artifact is required")
	}
	terminal, hasTerminal := observations["terminal"]
	expectedTerminal := ""
	if hasTerminal {
		expectedTerminal = terminalArtifactPath(index.Artifacts)
	}
	if index.TerminalReference != expectedTerminal {
		return errors.New("terminal index terminal reference is invalid")
	}
	if hasTerminal {
		rootTotal := rootObservation.completed + rootObservation.ready + rootObservation.blocked + rootObservation.failed
		terminalTotal := terminal.completed + terminal.ready + terminal.blocked + terminal.failed
		if terminal.completed < rootObservation.completed || (rootTotal > 0 && rootTotal != terminalTotal) {
			return errors.New("terminal index source counts are non-monotonic")
		}
	}

	expected := recomputeTerminalIndex(index, observations, rootObservation, terminal, hasTerminal)
	if index.Counts != expected.Counts {
		return errors.New("terminal index counts are contradictory")
	}
	if index.Lease != expected.Lease {
		return errors.New("terminal index lease status is contradictory")
	}
	if index.CompletionObserved != expected.CompletionObserved {
		return errors.New("terminal index completion observation is contradictory")
	}
	if index.CanonicalEvidenceAgreement != expected.CanonicalEvidenceAgreement {
		return errors.New("terminal index canonical agreement is contradictory")
	}
	if !sameTerminalStrings(index.ConflictCodes, expected.ConflictCodes) {
		if terminal.final && (terminal.ready > 0 || terminal.blocked > 0 || terminal.failed > 0) {
			return errors.New("terminal index permits final response while work remains")
		}
		return fmt.Errorf("terminal index conflict codes are contradictory: got %v want %v", index.ConflictCodes, expected.ConflictCodes)
	}
	if index.ReadinessPassed != expected.ReadinessPassed ||
		index.FinalResponseAllowed != expected.FinalResponseAllowed ||
		index.ReturnGateStatus != expected.ReturnGateStatus {
		return errors.New("terminal index readiness or final response conclusion is contradictory")
	}
	if index.ExactNextAction != expected.ExactNextAction {
		return errors.New("terminal index exact next action is contradictory")
	}
	return nil
}

func recomputeTerminalIndex(
	source CanonicalTerminalIndex,
	observations map[string]terminalIndexObservation,
	rootObservation, terminal terminalIndexObservation,
	hasTerminal bool,
) CanonicalTerminalIndex {
	result := CanonicalTerminalIndex{}
	leaseAuthority := observations["lease"]
	result.Counts.Minimum = firstTerminalPositive(
		leaseAuthority.minimumNodes, terminal.minimumNodes, rootObservation.minimumNodes,
	)
	minimumMinutesSet := leaseAuthority.minimumMinutesSet
	result.Lease.MinimumMinutes = leaseAuthority.minimumMinutes
	if !minimumMinutesSet {
		minimumMinutesSet = rootObservation.minimumMinutesSet
		result.Lease.MinimumMinutes = rootObservation.minimumMinutes
	}
	result.Lease.TargetMinutes = firstTerminalPositive(leaseAuthority.targetMinutes, rootObservation.targetMinutes)
	result.Lease.MaximumMinutes = firstTerminalPositive(leaseAuthority.maximumMinutes, rootObservation.maximumMinutes)
	if !hasTerminal {
		result.Counts.Completed = rootObservation.completed
		result.Counts.Ready = rootObservation.ready
		result.Counts.Blocked = rootObservation.blocked
		result.Counts.Failed = rootObservation.failed
		result.Counts.Total = result.Counts.Completed + result.Counts.Ready + result.Counts.Blocked + result.Counts.Failed
		if duration, ok := observations["duration"]; ok {
			if !minimumMinutesSet && duration.minimumMinutesSet {
				minimumMinutesSet = true
				result.Lease.MinimumMinutes = duration.minimumMinutes
			}
			result.Lease.TargetMinutes = firstTerminalPositive(result.Lease.TargetMinutes, duration.targetMinutes)
			result.Lease.MaximumMinutes = firstTerminalPositive(result.Lease.MaximumMinutes, duration.maximumMinutes)
			result.Lease.ElapsedMinutes = duration.elapsedMinutes
		}
		result.Lease.Status = "terminal_missing"
		result.ConflictCodes = append(result.ConflictCodes, "canonical_terminal_missing")
		if result.Lease.ElapsedMinutes <= 0 {
			result.ConflictCodes = append(result.ConflictCodes, "timing_unresolved")
		}
		if duration, ok := observations["duration"]; ok {
			if duration.nextAction != "" && duration.nextAction != rootObservation.nextAction {
				result.ConflictCodes = append(result.ConflictCodes, "duration_state_stale")
			}
		}
		if _, historical := observations["session"]; historical {
			result.ConflictCodes = append(result.ConflictCodes, "historical_snapshot_not_live")
		}
		result.ExactNextAction = "Treat these records as historical snapshots; create a new governed objective before scheduling work."
		result.ReturnGateStatus = "final_response_denied"
		sort.Strings(result.ConflictCodes)
		return result
	}

	result.Counts.Completed = terminal.completed
	result.Counts.Ready = terminal.ready
	result.Counts.Blocked = terminal.blocked
	result.Counts.Failed = terminal.failed
	result.Counts.Total = terminal.completed + terminal.ready + terminal.blocked + terminal.failed
	result.Lease.ElapsedMinutes = terminal.elapsedMinutes
	result.CompletionObserved = result.Counts.Minimum > 0 && terminal.completed >= result.Counts.Minimum
	result.CanonicalEvidenceAgreement = true
	switch {
	case !minimumMinutesSet || result.Lease.MinimumMinutes < 0 || result.Lease.MaximumMinutes <= 0 || result.Lease.MaximumMinutes < result.Lease.MinimumMinutes:
		result.Lease.Status = "invalid"
		result.ConflictCodes = append(result.ConflictCodes, "lease_window_invalid")
	case terminal.elapsedMinutes < result.Lease.MinimumMinutes:
		result.Lease.Status = "minimum_not_met"
		result.ConflictCodes = append(result.ConflictCodes, "lease_minimum_not_met")
	case terminal.elapsedMinutes > result.Lease.MaximumMinutes:
		result.Lease.Status = "maximum_exceeded"
		result.ConflictCodes = append(result.ConflictCodes, "lease_maximum_exceeded")
	default:
		result.Lease.Status = "within_window"
	}
	if result.Lease.TargetMinutes <= 0 {
		result.ConflictCodes = append(result.ConflictCodes, "lease_target_missing")
	}
	if terminal.leaseStatus != "" && terminal.leaseStatus != result.Lease.Status {
		result.ConflictCodes = append(result.ConflictCodes, "terminal_lease_status_mismatch")
	}
	if terminal.ready > 0 || terminal.blocked > 0 {
		result.ConflictCodes = append(result.ConflictCodes, "unfinished_work_final_response")
	}
	if terminal.failed > 0 {
		result.ConflictCodes = append(result.ConflictCodes, "failed_work_final_response")
	}
	if terminal.final && (terminal.ready > 0 || terminal.blocked > 0 || terminal.failed > 0 || result.Lease.Status != "within_window") {
		result.ConflictCodes = append(result.ConflictCodes, "terminal_final_response_allowed_despite_violation")
	}
	if terminal.final && !terminalNoAction(terminal.nextAction) {
		result.ConflictCodes = append(result.ConflictCodes, "terminal_exact_next_action_requires_execution")
	}
	if duration, ok := observations["duration"]; ok &&
		((duration.completed != 0 && duration.completed != terminal.completed) ||
			(duration.elapsedMinutes != 0 && duration.elapsedMinutes != terminal.elapsedMinutes)) {
		result.ConflictCodes = append(result.ConflictCodes, "duration_state_stale")
	}
	sort.Strings(result.ConflictCodes)
	result.ConflictCodes = uniqueTerminalStrings(result.ConflictCodes)
	result.ReadinessPassed = result.CompletionObserved && len(result.ConflictCodes) == 0 && !terminalIndexSafetyEnabled(terminal.safety)
	result.FinalResponseAllowed = result.ReadinessPassed && terminal.final
	if result.FinalResponseAllowed {
		result.ReturnGateStatus = "final_response_allowed"
		result.ExactNextAction = strings.TrimSpace(terminal.nextAction)
		if terminalNoAction(result.ExactNextAction) {
			result.ExactNextAction = "none"
		}
	} else {
		result.ReturnGateStatus = "final_response_denied"
		result.ExactNextAction = "Review the canonical conflict codes and produce a fresh governed terminal observation."
	}
	_ = source
	return result
}

func BuildHistoricalMissionTerminalIndex(root, generatedAt string) (CanonicalTerminalIndex, error) {
	if _, err := time.Parse(time.RFC3339, generatedAt); err != nil {
		return CanonicalTerminalIndex{}, errors.New("historical index timestamp must be RFC3339")
	}
	specs := []struct {
		role, state, path string
	}{
		{"root", "initial_snapshot", "workgraph.json"},
		{"duration", "duration_snapshot", "duration-ledger.json"},
		{"session", "supporting_snapshot", "codex-session-duration-readback.json"},
	}
	index := CanonicalTerminalIndex{
		ContractVersion:   TerminalIndexContract,
		SchemaDigest:      digestBytes([]byte(TerminalIndexContract)),
		MissionID:         "ao-mission-doubled-wave-v01",
		EvidenceRoot:      ".",
		GeneratedAtUTC:    generatedAt,
		ConflictSummaries: []string{},
		SafetyBoundaries:  TerminalIndexSafety{},
	}
	for sequence, spec := range specs {
		path := filepath.Join(root, spec.path)
		body, err := readTerminalRegularFile(path)
		if err != nil {
			return CanonicalTerminalIndex{}, err
		}
		var raw map[string]any
		if err := decodeTerminalJSON(body, &raw); err != nil {
			return CanonicalTerminalIndex{}, err
		}
		index.Artifacts = append(index.Artifacts, TerminalIndexArtifact{
			Role: spec.role, Sequence: sequence, Path: spec.path,
			Schema: terminalFirstString(raw, "schema", "contract_version"),
			SHA256: digestBytes(body), State: spec.state,
		})
		if sequence > 0 {
			index.Lineage = append(index.Lineage, TerminalIndexLineage{
				FromSequence: sequence - 1, ToSequence: sequence, Relation: "precedes",
			})
		}
	}
	observations := map[string]terminalIndexObservation{}
	for _, artifact := range index.Artifacts {
		body, _ := readTerminalRegularFile(filepath.Join(root, artifact.Path))
		var raw map[string]any
		_ = decodeTerminalJSON(body, &raw)
		observations[artifact.Role] = observeTerminalIndexArtifact(raw)
	}
	recomputed := recomputeTerminalIndex(index, observations, observations["root"], terminalIndexObservation{}, false)
	index.Counts = recomputed.Counts
	index.Lease = recomputed.Lease
	index.CompletionObserved = recomputed.CompletionObserved
	index.CanonicalEvidenceAgreement = recomputed.CanonicalEvidenceAgreement
	index.ReadinessPassed = recomputed.ReadinessPassed
	index.ReturnGateStatus = recomputed.ReturnGateStatus
	index.FinalResponseAllowed = recomputed.FinalResponseAllowed
	index.ConflictCodes = recomputed.ConflictCodes
	index.ExactNextAction = recomputed.ExactNextAction
	for _, code := range index.ConflictCodes {
		index.ConflictSummaries = append(index.ConflictSummaries, strings.ReplaceAll(code, "_", " "))
	}
	signCanonicalTerminalIndex(&index)
	return index, nil
}

func loadCanonicalTerminalIndex(path string) (CanonicalTerminalIndex, error) {
	var index CanonicalTerminalIndex
	body, err := readTerminalRegularFile(path)
	if err != nil {
		return index, err
	}
	if err := decodeTerminalJSON(body, &index); err != nil {
		return index, err
	}
	return index, nil
}

func decodeTerminalJSON(body []byte, target any) error {
	if err := validateNoDuplicateJSONKeys(body); err != nil {
		if !strings.Contains(err.Error(), "duplicate JSON key") {
			return fmt.Errorf("invalid JSON: %w", err)
		}
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func readTerminalRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("terminal index input must be a regular file")
	}
	if info.Size() > terminalIndexMaxFileBytes {
		return nil, errors.New("terminal index input exceeds size limit")
	}
	return os.ReadFile(path)
}

func terminalContainedPath(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe artifact path %q", relative)
	}
	path := filepath.Join(root, relative)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe artifact path %q", relative)
	}
	return path, nil
}

func observeTerminalIndexArtifact(raw map[string]any) terminalIndexObservation {
	counts, _ := raw["counts"].(map[string]any)
	supervisor, _ := raw["supervisor"].(map[string]any)
	safety, _ := raw["safety_boundaries"].(map[string]any)
	return terminalIndexObservation{
		missionID:         terminalFirstString(raw, "mission_id", "mission"),
		schema:            terminalFirstString(raw, "schema", "contract_version"),
		completed:         terminalFirstInt(raw, counts, "completed", "completed_nodes"),
		ready:             terminalFirstInt(raw, counts, "ready", "ready_nodes"),
		blocked:           terminalFirstInt(raw, counts, "blocked", "blocked_nodes"),
		failed:            terminalFirstInt(raw, counts, "failed", "failed_nodes"),
		minimumNodes:      terminalFirstInt(raw, supervisor, "minimum_nodes", "min_nodes"),
		minimumMinutes:    terminalFirstInt(raw, supervisor, "minimum_minutes", "min_minutes"),
		minimumMinutesSet: terminalHasInt(raw, supervisor, "minimum_minutes", "min_minutes"),
		targetMinutes:     terminalFirstInt(raw, supervisor, "target_minutes"),
		maximumMinutes:    terminalFirstInt(raw, supervisor, "maximum_minutes", "max_minutes"),
		elapsedMinutes:    terminalFirstInt(raw, nil, "elapsed_minutes", "elapsed_minutes_observed"),
		leaseStatus:       terminalString(raw, "lease_time_status"),
		final:             terminalBool(raw, "final_response_allowed"),
		nextAction:        terminalString(raw, "exact_next_action"),
		safety: TerminalIndexSafety{
			ExecutesWork:        terminalBool(raw, "executes_work") || terminalBool(safety, "executes_work"),
			ApprovesWork:        terminalBool(raw, "approves_work") || terminalBool(safety, "approves_work"),
			MutatesRepositories: terminalBool(raw, "mutates_repositories") || terminalBool(safety, "mutates_repositories"),
			CallsProviders:      terminalBool(raw, "calls_providers") || terminalBool(safety, "calls_providers"),
			Publishes:           terminalBool(raw, "publishes") || terminalBool(safety, "publishes"),
			Releases:            terminalBool(raw, "releases") || terminalBool(safety, "releases"),
			Deploys:             terminalBool(raw, "deploys") || terminalBool(safety, "deploys"),
			AdvancesAuthority:   terminalBool(raw, "advances_authority") || terminalBool(raw, "claims_authority_advance") || terminalBool(safety, "advances_authority"),
		},
	}
}

func signCanonicalTerminalIndex(index *CanonicalTerminalIndex) {
	index.Digest = ""
	body, _ := json.Marshal(*index)
	index.Digest = digestBytes(body)
}

func signTerminalIndexImport(readback *TerminalIndexImportReadback) {
	readback.StateDigest = ""
	body, _ := json.Marshal(*readback)
	readback.StateDigest = digestBytes(body)
}

func terminalImportStatus(index CanonicalTerminalIndex) string {
	if index.ReadinessPassed {
		return "reconciled"
	}
	if containsTerminalString(index.ConflictCodes, "canonical_terminal_missing") {
		return "no_canonical_terminal"
	}
	return "reconciled_fail_closed"
}

func validTerminalArtifactState(role, state string) bool {
	expected := map[string]string{
		"lease": "lease_authority", "root": "initial_snapshot", "checkpoint": "checkpoint",
		"duration": "duration_snapshot", "terminal": "terminal_candidate",
		"closure": "closure_support", "session": "supporting_snapshot",
	}
	return expected[role] == state
}

func terminalIndexSafetyEnabled(safety TerminalIndexSafety) bool {
	return safety.ExecutesWork || safety.ApprovesWork || safety.MutatesRepositories ||
		safety.CallsProviders || safety.Publishes || safety.Releases ||
		safety.Deploys || safety.AdvancesAuthority
}

func terminalArtifactPath(artifacts []TerminalIndexArtifact) string {
	for _, artifact := range artifacts {
		if artifact.Role == "terminal" {
			return artifact.Path
		}
	}
	return ""
}

func terminalString(raw map[string]any, key string) string {
	value, _ := raw[key].(string)
	return value
}

func terminalFirstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := terminalString(raw, key); value != "" {
			return value
		}
	}
	return ""
}

func terminalBool(raw map[string]any, key string) bool {
	value, _ := raw[key].(bool)
	return value
}

func terminalFirstInt(primary, secondary map[string]any, keys ...string) int {
	for _, source := range []map[string]any{primary, secondary} {
		for _, key := range keys {
			switch value := source[key].(type) {
			case float64:
				return int(value)
			case json.Number:
				var result int
				_, _ = fmt.Sscanf(value.String(), "%d", &result)
				return result
			}
		}
	}
	return 0
}

func terminalHasInt(primary, secondary map[string]any, keys ...string) bool {
	for _, source := range []map[string]any{primary, secondary} {
		for _, key := range keys {
			switch source[key].(type) {
			case float64, json.Number:
				return true
			}
		}
	}
	return false
}

func firstTerminalPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func terminalNoAction(action string) bool {
	action = strings.TrimSpace(strings.ToLower(action))
	return action == "" || action == "none" || action == "no further action" ||
		action == strings.ToLower(soakCanaryCompletedNextAction) ||
		action == strings.ToLower(fresh60CompletedNextAction)
}

func containsTerminalString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sameTerminalStrings(left, right []string) bool {
	leftCopy := append([]string{}, left...)
	rightCopy := append([]string{}, right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	return strings.Join(uniqueTerminalStrings(leftCopy), "\x00") == strings.Join(uniqueTerminalStrings(rightCopy), "\x00")
}

func uniqueTerminalStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
