package mission

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	IssueRepairSupervisorSchema = "ao.mission.github-issue-repair-supervisor.v1"
	IssueRepairEventSchema      = "ao.architecture.autonomous-issue-repair.event.v1"
	IssueRepairCheckpointSchema = "ao.architecture.autonomous-issue-repair.checkpoint.v1"
)

var (
	issueRepairIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{7,127}$`)
	issueRepairDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	issueRepairReasonPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,127}$`)
	issueRepairActorPattern  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

type IssueRepairSupervisorRequest struct {
	RunID                    string   `json:"run_id"`
	RunEnvelopeDigest        string   `json:"run_envelope_digest"`
	Actor                    string   `json:"actor"`
	LeaseID                  string   `json:"lease_id"`
	LeaseOwner               string   `json:"lease_owner"`
	LeaseExpiresAt           string   `json:"lease_expires_at"`
	EventType                string   `json:"event_type"`
	InputDigests             []string `json:"input_digests"`
	OutputDigests            []string `json:"output_digests"`
	ReasonCode               string   `json:"reason_code"`
	ExpectedCheckpointDigest string   `json:"expected_checkpoint_digest,omitempty"`
	EventBudget              int      `json:"event_budget"`
}

type IssueRepairLease struct {
	LeaseID                   string   `json:"lease_id"`
	Owner                     string   `json:"owner"`
	Status                    string   `json:"status"`
	ExpiresAt                 string   `json:"expires_at"`
	OwnershipVerifiedAt       string   `json:"ownership_verified_at"`
	PreviousWorkerActive      bool     `json:"previous_worker_active"`
	SuccessorResumeAuthorized bool     `json:"successor_resume_authorized"`
	AuthorizedEventActors     []string `json:"authorized_event_actors"`
}

type IssueRepairEvent struct {
	Schema              string   `json:"schema"`
	RunID               string   `json:"run_id"`
	RunEnvelopeDigest   string   `json:"run_envelope_digest"`
	Sequence            int      `json:"sequence"`
	PreviousEventDigest *string  `json:"previous_event_digest"`
	EventDigest         string   `json:"event_digest"`
	Actor               string   `json:"actor"`
	LeaseID             string   `json:"lease_id"`
	EventType           string   `json:"event_type"`
	InputDigests        []string `json:"input_digests"`
	OutputDigests       []string `json:"output_digests"`
	ReasonCode          string   `json:"reason_code"`
	Timestamp           string   `json:"timestamp"`
}

type IssueRepairCheckpoint struct {
	Schema            string           `json:"schema"`
	RunID             string           `json:"run_id"`
	RunEnvelopeDigest string           `json:"run_envelope_digest"`
	LastEventSequence int              `json:"last_event_sequence"`
	LastEventDigest   string           `json:"last_event_digest"`
	StateDigest       string           `json:"state_digest"`
	Lease             IssueRepairLease `json:"lease"`
	CheckpointDigest  string           `json:"checkpoint_digest"`
	CreatedAt         string           `json:"created_at"`
}

type IssueRepairSupervisorState struct {
	Schema              string                `json:"schema"`
	RunID               string                `json:"run_id"`
	RunEnvelopeDigest   string                `json:"run_envelope_digest"`
	Status              string                `json:"status"`
	EventBudget         int                   `json:"event_budget"`
	Events              []IssueRepairEvent    `json:"events"`
	Lease               IssueRepairLease      `json:"lease"`
	Checkpoint          IssueRepairCheckpoint `json:"checkpoint"`
	ExactNextAction     string                `json:"exact_next_action"`
	SafeToExecute       bool                  `json:"safe_to_execute"`
	ExecutesWork        bool                  `json:"executes_work"`
	ApprovesWork        bool                  `json:"approves_work"`
	MutatesRepositories bool                  `json:"mutates_repositories"`
	GeneratedAtUTC      string                `json:"generated_at_utc"`
}

func SuperviseIssueRepair(s Store, missionID string, request IssueRepairSupervisorRequest) (IssueRepairSupervisorState, error) {
	if err := validateIssueRepairRequest(request); err != nil {
		return IssueRepairSupervisorState{}, err
	}
	currentTime := time.Now().UTC()
	if s.Clock != nil {
		currentTime = s.Clock().UTC()
	}
	requestExpiry, _ := time.Parse(time.RFC3339, request.LeaseExpiresAt)
	if !requestExpiry.After(currentTime) {
		return IssueRepairSupervisorState{}, errors.New("issue repair lease expired")
	}
	var state IssueRepairSupervisorState
	_, err := s.updateWithCheckpointTransaction(missionID, func(record *Record) error {
		current := record.Evidence.IssueRepairSupervisor
		if current != nil {
			if err := ValidateIssueRepairSupervisorState(*current); err != nil {
				return err
			}
			if current.RunID != request.RunID || current.RunEnvelopeDigest != request.RunEnvelopeDigest {
				return errors.New("issue repair run identity mismatch")
			}
			if current.Lease.Status == "active" && current.Lease.LeaseID != request.LeaseID {
				return errors.New("issue repair lease conflict: another worker owns the active lease")
			}
			if current.Lease.Owner != request.LeaseOwner ||
				current.Lease.ExpiresAt != request.LeaseExpiresAt ||
				len(current.Lease.AuthorizedEventActors) != 1 ||
				current.Lease.AuthorizedEventActors[0] != request.Actor {
				return errors.New("issue repair lease ownership mismatch")
			}
			if request.EventBudget != current.EventBudget {
				return errors.New("issue repair event budget mismatch")
			}
			if issueRepairRequestMatchesLastEvent(request, *current) {
				matches, err := issueRepairReplayCheckpointMatches(request, *current)
				if err != nil {
					return err
				}
				if !matches {
					return errors.New("issue repair checkpoint digest mismatch")
				}
				state = *current
				return nil
			}
			if request.ExpectedCheckpointDigest != "" &&
				request.ExpectedCheckpointDigest != current.Checkpoint.CheckpointDigest {
				return errors.New("issue repair checkpoint digest mismatch")
			}
		}
		if current == nil {
			if request.EventType != "run_started" {
				return errors.New("issue repair supervisor must begin with run_started")
			}
			state = newIssueRepairSupervisorState(s, request)
			if _, err := ensureGoalLease(record, ContinueOptions{
				MinNodes:         1,
				MaxIterations:    request.EventBudget,
				CheckpointPolicy: "after_each_issue_repair_event",
			}); err != nil {
				return err
			}
		} else {
			state = *current
			if request.ExpectedCheckpointDigest != state.Checkpoint.CheckpointDigest {
				return errors.New("issue repair checkpoint digest mismatch")
			}
			if state.Status != "active" {
				return fmt.Errorf("issue repair supervisor is %s", state.Status)
			}
			if len(state.Events) >= state.EventBudget {
				return errors.New("issue repair event budget exhausted")
			}
		}

		stamp := now(s.Clock)
		event, err := buildIssueRepairEvent(state, request, stamp)
		if err != nil {
			return err
		}
		state.Events = append(append([]IssueRepairEvent(nil), state.Events...), event)
		state.Lease.OwnershipVerifiedAt = stamp
		state.ExactNextAction = "continue issue repair after " + request.EventType
		if request.EventType == "run_completed" {
			state.Status = "completed"
			state.Lease.Status = "closed"
			state.ExactNextAction = "verify terminal issue repair evidence"
		}
		if request.EventType == "run_blocked" {
			state.Status = "blocked"
			state.Lease.Status = "closed"
			state.ExactNextAction = "review the recorded issue repair blocker"
		}
		state.GeneratedAtUTC = stamp
		state.Checkpoint, err = buildIssueRepairCheckpoint(state, stamp)
		if err != nil {
			return err
		}
		record.Evidence.IssueRepairSupervisor = &state
		record.CurrentRoute = "ao-mission"
		record.CurrentPhase = "issue_repair_supervision"
		record.ExactNextAction = state.ExactNextAction
		step := ContinuationStep{
			Schema:          StepSchema,
			MissionID:       record.MissionID,
			CorrelationID:   record.CorrelationID,
			Iteration:       len(record.Steps) + 1,
			Route:           "ao-mission",
			Result:          request.EventType,
			ExactNextAction: "issue repair " + request.EventType + " persisted; " + state.ExactNextAction,
			GeneratedAtUTC:  stamp,
		}
		record.Steps = append(record.Steps, step)
		appendMissionCheckpoint(record, step)
		gate := EvaluateReturnGate(*record)
		record.ReturnGate = &gate
		return nil
	})
	return state, err
}

func newIssueRepairSupervisorState(s Store, request IssueRepairSupervisorRequest) IssueRepairSupervisorState {
	budget := request.EventBudget
	if budget <= 0 {
		budget = 64
	}
	stamp := now(s.Clock)
	return IssueRepairSupervisorState{
		Schema:            IssueRepairSupervisorSchema,
		RunID:             request.RunID,
		RunEnvelopeDigest: request.RunEnvelopeDigest,
		Status:            "active",
		EventBudget:       budget,
		Events:            []IssueRepairEvent{},
		Lease: IssueRepairLease{
			LeaseID:                   request.LeaseID,
			Owner:                     request.LeaseOwner,
			Status:                    "active",
			ExpiresAt:                 request.LeaseExpiresAt,
			OwnershipVerifiedAt:       stamp,
			PreviousWorkerActive:      false,
			SuccessorResumeAuthorized: false,
			AuthorizedEventActors:     []string{request.Actor},
		},
		SafeToExecute:       false,
		ExecutesWork:        false,
		ApprovesWork:        false,
		MutatesRepositories: false,
		GeneratedAtUTC:      stamp,
	}
}

func buildIssueRepairEvent(state IssueRepairSupervisorState, request IssueRepairSupervisorRequest, stamp string) (IssueRepairEvent, error) {
	var previous *string
	if len(state.Events) > 0 {
		value := state.Events[len(state.Events)-1].EventDigest
		previous = &value
	}
	event := IssueRepairEvent{
		Schema:              IssueRepairEventSchema,
		RunID:               request.RunID,
		RunEnvelopeDigest:   request.RunEnvelopeDigest,
		Sequence:            len(state.Events) + 1,
		PreviousEventDigest: previous,
		Actor:               request.Actor,
		LeaseID:             request.LeaseID,
		EventType:           request.EventType,
		InputDigests:        append([]string(nil), request.InputDigests...),
		OutputDigests:       append([]string(nil), request.OutputDigests...),
		ReasonCode:          request.ReasonCode,
		Timestamp:           stamp,
	}
	digest, err := canonicalIssueRepairDigest(event, "event_digest")
	if err != nil {
		return IssueRepairEvent{}, err
	}
	event.EventDigest = digest
	return event, nil
}

func buildIssueRepairCheckpoint(state IssueRepairSupervisorState, stamp string) (IssueRepairCheckpoint, error) {
	last := state.Events[len(state.Events)-1]
	stateDigest, err := canonicalIssueRepairDigest(struct {
		RunID       string             `json:"run_id"`
		Status      string             `json:"status"`
		EventBudget int                `json:"event_budget"`
		Events      []IssueRepairEvent `json:"events"`
		Lease       IssueRepairLease   `json:"lease"`
	}{
		RunID: state.RunID, Status: state.Status, EventBudget: state.EventBudget,
		Events: state.Events, Lease: state.Lease,
	}, "")
	if err != nil {
		return IssueRepairCheckpoint{}, err
	}
	checkpoint := IssueRepairCheckpoint{
		Schema:            IssueRepairCheckpointSchema,
		RunID:             state.RunID,
		RunEnvelopeDigest: state.RunEnvelopeDigest,
		LastEventSequence: last.Sequence,
		LastEventDigest:   last.EventDigest,
		StateDigest:       stateDigest,
		Lease:             state.Lease,
		CreatedAt:         stamp,
	}
	checkpointDigest, err := canonicalIssueRepairDigest(checkpoint, "checkpoint_digest")
	if err != nil {
		return IssueRepairCheckpoint{}, err
	}
	checkpoint.CheckpointDigest = checkpointDigest
	return checkpoint, nil
}

func issueRepairRequestMatchesLastEvent(request IssueRepairSupervisorRequest, state IssueRepairSupervisorState) bool {
	if len(state.Events) == 0 {
		return false
	}
	last := state.Events[len(state.Events)-1]
	return last.RunID == request.RunID &&
		last.RunEnvelopeDigest == request.RunEnvelopeDigest &&
		last.Actor == request.Actor &&
		last.LeaseID == request.LeaseID &&
		last.EventType == request.EventType &&
		last.ReasonCode == request.ReasonCode &&
		stringSlicesEqual(last.InputDigests, request.InputDigests) &&
		stringSlicesEqual(last.OutputDigests, request.OutputDigests)
}

func issueRepairReplayCheckpointMatches(
	request IssueRepairSupervisorRequest,
	state IssueRepairSupervisorState,
) (bool, error) {
	if len(state.Events) == 1 {
		return request.ExpectedCheckpointDigest == "", nil
	}
	previousEvents := append([]IssueRepairEvent(nil), state.Events[:len(state.Events)-1]...)
	previous := previousEvents[len(previousEvents)-1]
	prior := state
	prior.Status = "active"
	prior.Events = previousEvents
	prior.Lease.Status = "active"
	prior.Lease.OwnershipVerifiedAt = previous.Timestamp
	prior.GeneratedAtUTC = previous.Timestamp
	checkpoint, err := buildIssueRepairCheckpoint(prior, previous.Timestamp)
	if err != nil {
		return false, err
	}
	return request.ExpectedCheckpointDigest == checkpoint.CheckpointDigest, nil
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validateIssueRepairRequest(request IssueRepairSupervisorRequest) error {
	if !issueRepairIDPattern.MatchString(request.RunID) ||
		!issueRepairIDPattern.MatchString(request.LeaseID) {
		return errors.New("issue repair run and lease IDs must be bounded lowercase identifiers")
	}
	if !issueRepairDigestPattern.MatchString(request.RunEnvelopeDigest) {
		return errors.New("issue repair run envelope digest must be lowercase SHA-256")
	}
	if !issueRepairActorPattern.MatchString(request.Actor) ||
		!issueRepairActorPattern.MatchString(request.LeaseOwner) {
		return errors.New("issue repair actor and lease owner must be bounded ASCII identifiers")
	}
	expiresAt, err := time.Parse(time.RFC3339, request.LeaseExpiresAt)
	if err != nil || expiresAt.IsZero() {
		return errors.New("issue repair lease expiry must be RFC3339")
	}
	if !issueRepairEventTypeAllowed(request.EventType) {
		return errors.New("issue repair event type is not allowed")
	}
	if !issueRepairReasonPattern.MatchString(request.ReasonCode) {
		return errors.New("issue repair reason code is malformed")
	}
	if request.EventBudget < 1 || request.EventBudget > 10000 {
		return errors.New("issue repair event budget must be between 1 and 10000")
	}
	if len(request.InputDigests) > 64 || len(request.OutputDigests) > 64 {
		return errors.New("issue repair event digest lists exceed 64 entries")
	}
	if err := validateIssueRepairDigestList(request.InputDigests); err != nil {
		return err
	}
	if err := validateIssueRepairDigestList(request.OutputDigests); err != nil {
		return err
	}
	if request.ExpectedCheckpointDigest != "" &&
		!issueRepairDigestPattern.MatchString(request.ExpectedCheckpointDigest) {
		return errors.New("issue repair expected checkpoint digest must be lowercase SHA-256")
	}
	return nil
}

func ValidateIssueRepairSupervisorState(state IssueRepairSupervisorState) error {
	if state.Schema != IssueRepairSupervisorSchema || state.EventBudget < 1 ||
		len(state.Events) < 1 || len(state.Events) > state.EventBudget {
		return errors.New("issue repair supervisor state structure is invalid")
	}
	if !issueRepairIDPattern.MatchString(state.RunID) ||
		!issueRepairDigestPattern.MatchString(state.RunEnvelopeDigest) ||
		strings.TrimSpace(state.ExactNextAction) == "" {
		return errors.New("issue repair supervisor state identity is invalid")
	}
	if state.SafeToExecute || state.ExecutesWork || state.ApprovesWork ||
		state.MutatesRepositories {
		return errors.New("issue repair supervisor state widened authority")
	}
	if err := validateIssueRepairLease(state.Lease, state.Status); err != nil {
		return err
	}
	checkpointCreated, err := time.Parse(time.RFC3339, state.Checkpoint.CreatedAt)
	if err != nil {
		return errors.New("issue repair checkpoint creation time is invalid")
	}
	var previous *string
	for index, event := range state.Events {
		if event.Schema != IssueRepairEventSchema ||
			event.RunID != state.RunID ||
			event.RunEnvelopeDigest != state.RunEnvelopeDigest ||
			event.Sequence != index+1 ||
			!optionalStringEqual(event.PreviousEventDigest, previous) {
			return errors.New("issue repair event chain identity mismatch")
		}
		if !issueRepairIDPattern.MatchString(event.LeaseID) ||
			event.LeaseID != state.Lease.LeaseID ||
			!issueRepairActorPattern.MatchString(event.Actor) ||
			!issueRepairStringSliceContains(state.Lease.AuthorizedEventActors, event.Actor) ||
			!issueRepairEventTypeAllowed(event.EventType) ||
			!issueRepairReasonPattern.MatchString(event.ReasonCode) ||
			validateIssueRepairDigestList(event.InputDigests) != nil ||
			validateIssueRepairDigestList(event.OutputDigests) != nil {
			return errors.New("issue repair event semantics are invalid")
		}
		if (index == 0 && event.EventType != "run_started") ||
			(index > 0 && event.EventType == "run_started") ||
			(index < len(state.Events)-1 &&
				(event.EventType == "run_completed" || event.EventType == "run_blocked")) {
			return errors.New("issue repair event lifecycle order is invalid")
		}
		eventTime, parseErr := time.Parse(time.RFC3339, event.Timestamp)
		if parseErr != nil || eventTime.After(checkpointCreated) {
			return errors.New("issue repair event timestamp is invalid")
		}
		expectedDigest, err := canonicalIssueRepairDigest(event, "event_digest")
		if err != nil || event.EventDigest != expectedDigest {
			return errors.New("issue repair event digest mismatch")
		}
		value := event.EventDigest
		previous = &value
	}
	if state.Checkpoint.Schema != IssueRepairCheckpointSchema ||
		state.Checkpoint.RunID != state.RunID ||
		state.Checkpoint.RunEnvelopeDigest != state.RunEnvelopeDigest ||
		state.Checkpoint.LastEventSequence != len(state.Events) ||
		state.Checkpoint.LastEventDigest != state.Events[len(state.Events)-1].EventDigest {
		return errors.New("issue repair checkpoint identity mismatch")
	}
	lastEventType := state.Events[len(state.Events)-1].EventType
	if (state.Status == "completed" && lastEventType != "run_completed") ||
		(state.Status == "blocked" && lastEventType != "run_blocked") ||
		(state.Status == "active" &&
			(lastEventType == "run_completed" || lastEventType == "run_blocked")) {
		return errors.New("issue repair terminal status and event mismatch")
	}
	verifiedAt, _ := time.Parse(time.RFC3339, state.Lease.OwnershipVerifiedAt)
	expiresAt, _ := time.Parse(time.RFC3339, state.Lease.ExpiresAt)
	if verifiedAt.After(checkpointCreated) ||
		(state.Lease.Status == "active" && !expiresAt.After(checkpointCreated)) {
		return errors.New("issue repair checkpoint lease timing is invalid")
	}
	stateLeaseBody, _ := json.Marshal(state.Lease)
	checkpointLeaseBody, _ := json.Marshal(state.Checkpoint.Lease)
	if string(stateLeaseBody) != string(checkpointLeaseBody) {
		return errors.New("issue repair checkpoint lease mismatch")
	}
	expected, err := buildIssueRepairCheckpoint(state, state.Checkpoint.CreatedAt)
	storedDigest, storedErr := canonicalIssueRepairDigest(
		state.Checkpoint,
		"checkpoint_digest",
	)
	if err != nil || storedErr != nil ||
		state.Checkpoint.CheckpointDigest != storedDigest ||
		state.Checkpoint.CheckpointDigest != expected.CheckpointDigest ||
		state.Checkpoint.StateDigest != expected.StateDigest {
		return errors.New("issue repair checkpoint integrity mismatch")
	}
	return nil
}

func validateIssueRepairLease(lease IssueRepairLease, stateStatus string) error {
	if !issueRepairIDPattern.MatchString(lease.LeaseID) ||
		!issueRepairActorPattern.MatchString(lease.Owner) ||
		len(lease.AuthorizedEventActors) < 1 {
		return errors.New("issue repair lease identity is invalid")
	}
	actors := map[string]bool{}
	for _, actor := range lease.AuthorizedEventActors {
		if !issueRepairActorPattern.MatchString(actor) || actors[actor] {
			return errors.New("issue repair lease actors are invalid")
		}
		actors[actor] = true
	}
	expiresAt, expiryErr := time.Parse(time.RFC3339, lease.ExpiresAt)
	verifiedAt, verifiedErr := time.Parse(time.RFC3339, lease.OwnershipVerifiedAt)
	if expiryErr != nil || verifiedErr != nil || verifiedAt.After(expiresAt) {
		return errors.New("issue repair lease timestamps are invalid")
	}
	switch stateStatus {
	case "active":
		if lease.Status != "active" {
			return errors.New("active issue repair state requires active lease")
		}
	case "completed", "blocked":
		if lease.Status != "closed" {
			return errors.New("terminal issue repair state requires closed lease")
		}
	default:
		return errors.New("issue repair supervisor status is invalid")
	}
	if lease.PreviousWorkerActive || lease.SuccessorResumeAuthorized {
		return errors.New("issue repair lease conflict flags are invalid")
	}
	return nil
}

func issueRepairEventTypeAllowed(eventType string) bool {
	switch eventType {
	case "run_started", "discovery_completed", "candidate_decided",
		"checkpoint_created", "handoff_started", "handoff_completed",
		"attention_required", "run_completed", "run_blocked":
		return true
	default:
		return false
	}
}

func issueRepairStringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateIssueRepairDigestList(values []string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if !issueRepairDigestPattern.MatchString(value) {
			return errors.New("issue repair event digests must be lowercase SHA-256")
		}
		if seen[value] {
			return errors.New("issue repair event digest lists must contain unique values")
		}
		seen[value] = true
	}
	return nil
}

func optionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func canonicalIssueRepairDigest(value any, omittedField string) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return "", err
	}
	if omittedField != "" {
		delete(document, omittedField)
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(digestBytes(canonical), "sha256:"), nil
}
