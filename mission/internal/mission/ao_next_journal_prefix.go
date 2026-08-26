package mission

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	aoNextJournalPrefixImportKind = "ao-next-journal-prefix"
	aoNextJournalPrefixInputLimit = 16 << 20
	aoNextJournalPrefixEventLimit = 4096
)

var aoNextJournalPrefixFields = map[string]string{
	"schema_version": "string", "run_id": "string", "request_digest": "string",
	"journal_identity": "object", "worker_count": "uint32", "dynamic_fanout": "boolean",
	"first_sequence": "uint64", "last_sequence": "uint64|null",
	"preceding_prefix_digest": "string|null", "events_digest": "string", "events": "array",
	"terminal_digest": "string|null", "terminal_record": "object|null",
	"safe_to_execute": "boolean", "executes_work": "boolean", "approves_work": "boolean",
	"mutates_repositories": "boolean", "grants_provider_access": "boolean",
	"publishes_artifacts": "boolean", "releases": "boolean", "deploys": "boolean",
	"advances_authority": "boolean", "prefix_digest": "string",
}

var aoNextJournalIdentityFields = map[string]string{
	"request_digest": "string", "source_digest": "string", "workspace_digest": "string",
	"policy_digest": "string", "model_profile_digest": "string", "verifier_profile_digest": "string",
}

var aoNextJournalEventFields = map[string]string{
	"schema_version": "string", "sequence": "uint64", "kind": "object",
}

var aoNextJournalEventKindFields = map[string]map[string]string{
	"provider_request_intent": {
		"kind": "string", "prepared_run_digest": "string", "execution_authority_digest": "string",
	},
	"provider_process_started":         {"kind": "string", "invocation_digest": "string"},
	"provider_output_retained":         {"kind": "string", "raw_capture_digest": "string"},
	"provider_capture_index_published": {"kind": "string", "index_digest": "string"},
	"provider_capture_verified":        {"kind": "string", "index_digest": "string"},
	"adapter_turn_normalized":          {"kind": "string", "turn_digest": "string"},
	"effect_intent":                    {"kind": "string", "effect_id": "string", "effect_digest": "string"},
	"effect_committed":                 {"kind": "string", "effect_id": "string"},
	"effect_completed":                 {"kind": "string", "observation": "object"},
	"verification_started":             {"kind": "string", "attempt": "uint32"},
	"verifier_recorded":                {"kind": "string", "report_digest": "string"},
	"terminal_published":               {"kind": "string", "record_digest": "string"},
}

var aoNextJournalEffectObservationFields = map[string]string{
	"effect_id": "string", "output_digest": "string", "status": "int32",
	"stderr": "array", "stdout": "array",
}

var aoNextJournalTerminalFields = map[string]string{
	"schema_version": "string", "variant": "string", "terminal_state": "string",
	"measurement": "object", "capture_digests": "array", "raw_capture_index_digest": "string",
	"verifier_report_digest": "string|null", "n7_execution_authority_digest": "string|null",
	"git_workspace": "object", "ao2_control_diagnostics": "array",
	"native_effect_observations": "array", "record_digest": "string",
}

var aoNextJournalTokenFields = map[string]string{
	"input_tokens": "uint64|null", "cached_input_tokens": "uint64|null",
	"reasoning_tokens": "uint64|null", "output_tokens": "uint64|null",
	"reported_total_tokens": "uint64",
}

var aoNextJournalMeasurementFields = map[string]string{
	"schema_version": "string", "corpus_digest": "string", "run_id": "string",
	"trial_id": "string", "trial_index": "uint32", "schedule_position": "uint32",
	"raw_capture_digest": "string", "raw_capture_digests": "array",
	"workspace_instance_id": "string", "task_id": "string", "variant": "string",
	"source_digest": "string", "objective_digest": "string", "workspace_seed_digest": "string",
	"visible_fixtures_digest": "string", "hidden_tests_digest": "string",
	"verifier_profile_digest": "string", "runtime": "string", "runtime_digest": "string",
	"model_identifier": "string", "model_digest": "string", "prompt_digest": "string",
	"policy_digest": "string", "adapter_version": "string", "adapter_digest": "string",
	"measurement_origin": "string", "provider_usage_trusted": "boolean", "tokens": "object",
	"wall_clock_ms": "uint64", "model_wait_ms": "uint64", "worker_turns": "uint32",
	"repair_attempts": "uint32", "operator_interventions": "uint32", "changed_files": "uint32",
	"accepted_changed_files": "uint32", "task_success": "boolean",
	"hidden_tests_passed": "uint32", "hidden_tests_total": "uint32", "regressions": "uint32",
	"unauthorized_effects": "uint32", "evidence_complete": "boolean",
	"evidence_digest_valid": "boolean", "recovery_attempted": "boolean",
	"recovery_no_duplicate_effect": "boolean", "cross_runtime_agreement": "boolean",
	"worker_count": "uint32", "dynamic_fanout": "boolean", "hidden_test_exposure": "boolean",
}

var aoNextJournalGitWorkspaceFields = map[string]string{
	"repository_root": "string", "common_dir": "string", "head_commit": "string",
	"branch": "string", "control_digest": "string", "index_digest": "string",
}

type aoNextJournalPrefix struct {
	SchemaVersion         string                       `json:"schema_version"`
	RunID                 string                       `json:"run_id"`
	RequestDigest         string                       `json:"request_digest"`
	JournalIdentity       aoNextJournalIdentity        `json:"journal_identity"`
	WorkerCount           uint32                       `json:"worker_count"`
	DynamicFanout         bool                         `json:"dynamic_fanout"`
	FirstSequence         uint64                       `json:"first_sequence"`
	LastSequence          *uint64                      `json:"last_sequence"`
	PrecedingPrefixDigest *string                      `json:"preceding_prefix_digest"`
	EventsDigest          string                       `json:"events_digest"`
	Events                []aoNextJournalEvent         `json:"events"`
	TerminalDigest        *string                      `json:"terminal_digest"`
	TerminalRecord        *aoNextJournalTerminalRecord `json:"terminal_record"`
	SafeToExecute         bool                         `json:"safe_to_execute"`
	ExecutesWork          bool                         `json:"executes_work"`
	ApprovesWork          bool                         `json:"approves_work"`
	MutatesRepositories   bool                         `json:"mutates_repositories"`
	GrantsProviderAccess  bool                         `json:"grants_provider_access"`
	PublishesArtifacts    bool                         `json:"publishes_artifacts"`
	Releases              bool                         `json:"releases"`
	Deploys               bool                         `json:"deploys"`
	AdvancesAuthority     bool                         `json:"advances_authority"`
	PrefixDigest          string                       `json:"prefix_digest"`
}

type aoNextJournalIdentity struct {
	RequestDigest         string `json:"request_digest"`
	SourceDigest          string `json:"source_digest"`
	WorkspaceDigest       string `json:"workspace_digest"`
	PolicyDigest          string `json:"policy_digest"`
	ModelProfileDigest    string `json:"model_profile_digest"`
	VerifierProfileDigest string `json:"verifier_profile_digest"`
}

type aoNextJournalEvent struct {
	SchemaVersion string                 `json:"schema_version"`
	Sequence      uint64                 `json:"sequence"`
	Kind          aoNextJournalEventKind `json:"kind"`
}

type aoNextJournalEventKind struct {
	Kind                     string                          `json:"kind"`
	PreparedRunDigest        string                          `json:"prepared_run_digest,omitempty"`
	ExecutionAuthorityDigest string                          `json:"execution_authority_digest,omitempty"`
	InvocationDigest         string                          `json:"invocation_digest,omitempty"`
	RawCaptureDigest         string                          `json:"raw_capture_digest,omitempty"`
	IndexDigest              string                          `json:"index_digest,omitempty"`
	TurnDigest               string                          `json:"turn_digest,omitempty"`
	EffectID                 string                          `json:"effect_id,omitempty"`
	EffectDigest             string                          `json:"effect_digest,omitempty"`
	Observation              *aoNextJournalEffectObservation `json:"observation,omitempty"`
	Attempt                  *uint32                         `json:"attempt,omitempty"`
	ReportDigest             string                          `json:"report_digest,omitempty"`
	RecordDigest             string                          `json:"record_digest,omitempty"`
}

type aoNextJournalEffectObservation struct {
	EffectID     string `json:"effect_id"`
	OutputDigest string `json:"output_digest"`
	Status       int32  `json:"status"`
	Stderr       []byte `json:"stderr"`
	Stdout       []byte `json:"stdout"`
}

type aoNextJournalTokenRow struct {
	InputTokens         *uint64 `json:"input_tokens"`
	CachedInputTokens   *uint64 `json:"cached_input_tokens"`
	ReasoningTokens     *uint64 `json:"reasoning_tokens"`
	OutputTokens        *uint64 `json:"output_tokens"`
	ReportedTotalTokens uint64  `json:"reported_total_tokens"`
}

type aoNextJournalMeasurement struct {
	SchemaVersion             string                `json:"schema_version"`
	CorpusDigest              string                `json:"corpus_digest"`
	RunID                     string                `json:"run_id"`
	TrialID                   string                `json:"trial_id"`
	TrialIndex                uint32                `json:"trial_index"`
	SchedulePosition          uint32                `json:"schedule_position"`
	RawCaptureDigest          string                `json:"raw_capture_digest"`
	RawCaptureDigests         []string              `json:"raw_capture_digests"`
	WorkspaceInstanceID       string                `json:"workspace_instance_id"`
	TaskID                    string                `json:"task_id"`
	Variant                   string                `json:"variant"`
	SourceDigest              string                `json:"source_digest"`
	ObjectiveDigest           string                `json:"objective_digest"`
	WorkspaceSeedDigest       string                `json:"workspace_seed_digest"`
	VisibleFixturesDigest     string                `json:"visible_fixtures_digest"`
	HiddenTestsDigest         string                `json:"hidden_tests_digest"`
	VerifierProfileDigest     string                `json:"verifier_profile_digest"`
	Runtime                   string                `json:"runtime"`
	RuntimeDigest             string                `json:"runtime_digest"`
	ModelIdentifier           string                `json:"model_identifier"`
	ModelDigest               string                `json:"model_digest"`
	PromptDigest              string                `json:"prompt_digest"`
	PolicyDigest              string                `json:"policy_digest"`
	AdapterVersion            string                `json:"adapter_version"`
	AdapterDigest             string                `json:"adapter_digest"`
	MeasurementOrigin         string                `json:"measurement_origin"`
	ProviderUsageTrusted      bool                  `json:"provider_usage_trusted"`
	Tokens                    aoNextJournalTokenRow `json:"tokens"`
	WallClockMS               uint64                `json:"wall_clock_ms"`
	ModelWaitMS               uint64                `json:"model_wait_ms"`
	WorkerTurns               uint32                `json:"worker_turns"`
	RepairAttempts            uint32                `json:"repair_attempts"`
	OperatorInterventions     uint32                `json:"operator_interventions"`
	ChangedFiles              uint32                `json:"changed_files"`
	AcceptedChangedFiles      uint32                `json:"accepted_changed_files"`
	TaskSuccess               bool                  `json:"task_success"`
	HiddenTestsPassed         uint32                `json:"hidden_tests_passed"`
	HiddenTestsTotal          uint32                `json:"hidden_tests_total"`
	Regressions               uint32                `json:"regressions"`
	UnauthorizedEffects       uint32                `json:"unauthorized_effects"`
	EvidenceComplete          bool                  `json:"evidence_complete"`
	EvidenceDigestValid       bool                  `json:"evidence_digest_valid"`
	RecoveryAttempted         bool                  `json:"recovery_attempted"`
	RecoveryNoDuplicateEffect bool                  `json:"recovery_no_duplicate_effect"`
	CrossRuntimeAgreement     bool                  `json:"cross_runtime_agreement"`
	WorkerCount               uint32                `json:"worker_count"`
	DynamicFanout             bool                  `json:"dynamic_fanout"`
	HiddenTestExposure        bool                  `json:"hidden_test_exposure"`
}

type aoNextJournalGitWorkspace struct {
	RepositoryRoot string `json:"repository_root"`
	CommonDir      string `json:"common_dir"`
	HeadCommit     string `json:"head_commit"`
	Branch         string `json:"branch"`
	ControlDigest  string `json:"control_digest"`
	IndexDigest    string `json:"index_digest"`
}

type aoNextJournalTerminalRecord struct {
	SchemaVersion              string                           `json:"schema_version"`
	Variant                    string                           `json:"variant"`
	TerminalState              string                           `json:"terminal_state"`
	Measurement                aoNextJournalMeasurement         `json:"measurement"`
	CaptureDigests             []string                         `json:"capture_digests"`
	RawCaptureIndexDigest      string                           `json:"raw_capture_index_digest"`
	VerifierReportDigest       *string                          `json:"verifier_report_digest"`
	N7ExecutionAuthorityDigest *string                          `json:"n7_execution_authority_digest"`
	GitWorkspace               aoNextJournalGitWorkspace        `json:"git_workspace"`
	AO2ControlDiagnostics      []any                            `json:"ao2_control_diagnostics"`
	NativeEffectObservations   []aoNextJournalEffectObservation `json:"native_effect_observations"`
	RecordDigest               string                           `json:"record_digest"`
}

type aoNextJournalLifecycle struct {
	providerStep     uint8
	effects          map[string]bool
	verificationSeen bool
	verificationOpen bool
	verifierRecords  uint32
	verifierDigest   string
	terminalState    string
}

func parseAONextJournalPrefix(body []byte) (aoNextJournalPrefix, error) {
	if len(body) > aoNextJournalPrefixInputLimit {
		return aoNextJournalPrefix{}, fmt.Errorf("AO Next journal prefix exceeds %d bytes", aoNextJournalPrefixInputLimit)
	}
	value, err := decodeExactJSON(body)
	if err != nil {
		return aoNextJournalPrefix{}, err
	}
	document, ok := value.(map[string]any)
	if !ok {
		return aoNextJournalPrefix{}, fmt.Errorf("AO Next journal prefix must be a JSON object")
	}
	if err := validateAONextCandidateFields(document, aoNextJournalPrefixFields, []string{
		"schema_version", "run_id", "request_digest", "journal_identity", "worker_count",
		"dynamic_fanout", "first_sequence", "last_sequence", "preceding_prefix_digest",
		"events_digest", "events", "terminal_digest", "terminal_record", "safe_to_execute",
		"executes_work", "approves_work", "mutates_repositories", "grants_provider_access",
		"publishes_artifacts", "releases", "deploys", "advances_authority", "prefix_digest",
	}, "AO Next journal prefix"); err != nil {
		return aoNextJournalPrefix{}, err
	}
	if err := validateAONextJournalNestedFields(document); err != nil {
		return aoNextJournalPrefix{}, err
	}
	var prefix aoNextJournalPrefix
	if err := decodeStrictJSONObject(body, &prefix, "AO Next journal prefix", aoNextJournalPrefixFields, []string{
		"schema_version", "run_id", "request_digest", "journal_identity", "worker_count",
		"dynamic_fanout", "first_sequence", "last_sequence", "preceding_prefix_digest",
		"events_digest", "events", "terminal_digest", "terminal_record", "safe_to_execute",
		"executes_work", "approves_work", "mutates_repositories", "grants_provider_access",
		"publishes_artifacts", "releases", "deploys", "advances_authority", "prefix_digest",
	}); err != nil {
		return aoNextJournalPrefix{}, err
	}
	if err := validateAONextJournalSemantics(prefix, document); err != nil {
		return aoNextJournalPrefix{}, err
	}
	return prefix, nil
}

func validateAONextJournalNestedFields(document map[string]any) error {
	identity, _ := document["journal_identity"].(map[string]any)
	if err := validateAONextCandidateFields(identity, aoNextJournalIdentityFields, []string{
		"request_digest", "source_digest", "workspace_digest", "policy_digest",
		"model_profile_digest", "verifier_profile_digest",
	}, "AO Next journal identity"); err != nil {
		return err
	}
	events, _ := document["events"].([]any)
	if len(events) > aoNextJournalPrefixEventLimit {
		return fmt.Errorf("AO Next journal prefix has more than %d events", aoNextJournalPrefixEventLimit)
	}
	for index, raw := range events {
		event, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("AO Next journal event %d must be an object", index)
		}
		name := fmt.Sprintf("AO Next journal event %d", index)
		if err := validateAONextCandidateFields(event, aoNextJournalEventFields, []string{"schema_version", "sequence", "kind"}, name); err != nil {
			return err
		}
		kind, _ := event["kind"].(map[string]any)
		discriminator := stringFromAny(kind["kind"])
		fields, known := aoNextJournalEventKindFields[discriminator]
		if !known {
			return fmt.Errorf("%s kind %q is unsupported", name, discriminator)
		}
		if err := validateAONextCandidateFields(kind, fields, mapKeys(fields), name+" kind"); err != nil {
			return err
		}
		if discriminator == "effect_completed" {
			if err := validateAONextJournalEffectObservation(kind["observation"], name+" observation"); err != nil {
				return err
			}
		}
	}
	terminalValue := document["terminal_record"]
	if terminalValue == nil {
		return nil
	}
	terminal, _ := terminalValue.(map[string]any)
	if err := validateAONextCandidateFields(terminal, aoNextJournalTerminalFields, mapKeys(aoNextJournalTerminalFields), "AO Next journal terminal"); err != nil {
		return err
	}
	measurement, _ := terminal["measurement"].(map[string]any)
	if err := validateAONextCandidateFields(measurement, aoNextJournalMeasurementFields, mapKeys(aoNextJournalMeasurementFields), "AO Next journal measurement"); err != nil {
		return err
	}
	tokens, _ := measurement["tokens"].(map[string]any)
	if err := validateAONextCandidateFields(tokens, aoNextJournalTokenFields, mapKeys(aoNextJournalTokenFields), "AO Next journal tokens"); err != nil {
		return err
	}
	workspace, _ := terminal["git_workspace"].(map[string]any)
	if err := validateAONextCandidateFields(workspace, aoNextJournalGitWorkspaceFields, mapKeys(aoNextJournalGitWorkspaceFields), "AO Next journal Git workspace"); err != nil {
		return err
	}
	for index, observation := range terminal["native_effect_observations"].([]any) {
		if err := validateAONextJournalEffectObservation(observation, fmt.Sprintf("AO Next journal native effect %d", index)); err != nil {
			return err
		}
	}
	return nil
}

func validateAONextJournalEffectObservation(value any, name string) error {
	observation, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", name)
	}
	if err := validateAONextCandidateFields(observation, aoNextJournalEffectObservationFields, mapKeys(aoNextJournalEffectObservationFields), name); err != nil {
		return err
	}
	for _, field := range []string{"stdout", "stderr"} {
		for _, value := range observation[field].([]any) {
			number, ok := strictJSONInteger(value)
			if !ok || number < 0 || number > math.MaxUint8 {
				return fmt.Errorf("%s field %q must contain bytes", name, field)
			}
		}
	}
	return nil
}

func validateAONextJournalSemantics(prefix aoNextJournalPrefix, document map[string]any) error {
	if prefix.SchemaVersion != "ao.next.execution-journal-prefix.v1" {
		return fmt.Errorf("AO Next journal prefix schema is unsupported")
	}
	if prefix.WorkerCount != 1 || prefix.DynamicFanout {
		return fmt.Errorf("AO Next journal prefix violates the one-worker boundary")
	}
	for name, enabled := range map[string]bool{
		"safe_to_execute": prefix.SafeToExecute, "executes_work": prefix.ExecutesWork,
		"approves_work": prefix.ApprovesWork, "mutates_repositories": prefix.MutatesRepositories,
		"grants_provider_access": prefix.GrantsProviderAccess, "publishes_artifacts": prefix.PublishesArtifacts,
		"releases": prefix.Releases, "deploys": prefix.Deploys, "advances_authority": prefix.AdvancesAuthority,
	} {
		if enabled {
			return fmt.Errorf("AO Next journal prefix enables authority boundary %q", name)
		}
	}
	if strings.TrimSpace(prefix.RunID) == "" {
		return fmt.Errorf("AO Next journal prefix run identity is required")
	}
	for _, digest := range []string{
		prefix.RequestDigest, prefix.JournalIdentity.RequestDigest, prefix.JournalIdentity.SourceDigest,
		prefix.JournalIdentity.WorkspaceDigest, prefix.JournalIdentity.PolicyDigest,
		prefix.JournalIdentity.ModelProfileDigest, prefix.JournalIdentity.VerifierProfileDigest,
		prefix.EventsDigest, prefix.PrefixDigest,
	} {
		if !sha256DigestPattern.MatchString(digest) {
			return fmt.Errorf("AO Next journal prefix digest is invalid")
		}
	}
	if prefix.RequestDigest != prefix.JournalIdentity.RequestDigest {
		return fmt.Errorf("AO Next journal prefix journal identity mismatched")
	}
	if prefix.FirstSequence != 0 || prefix.PrecedingPrefixDigest != nil {
		return fmt.Errorf("AO Next journal prefix sequence or preceding digest is invalid")
	}
	if len(prefix.Events) == 0 {
		if prefix.LastSequence != nil {
			return fmt.Errorf("AO Next journal prefix event sequence is invalid")
		}
	} else if prefix.LastSequence == nil || *prefix.LastSequence != uint64(len(prefix.Events)-1) {
		return fmt.Errorf("AO Next journal prefix event sequence is invalid")
	}
	for index, event := range prefix.Events {
		if event.SchemaVersion != "ao.next.journal-event.v1" || event.Sequence != uint64(index) {
			return fmt.Errorf("AO Next journal prefix event sequence is invalid")
		}
	}
	lifecycle, err := validateAONextJournalLifecycle(prefix.Events)
	if err != nil {
		return err
	}
	eventsDigest, err := canonicalAONextJournalDigest(document["events"])
	if err != nil || eventsDigest != prefix.EventsDigest {
		return fmt.Errorf("AO Next journal prefix event digest mismatched")
	}
	if err := validateAONextJournalTerminal(prefix, document, lifecycle); err != nil {
		return err
	}
	ordered := []any{
		document["schema_version"], document["run_id"], document["request_digest"], document["journal_identity"],
		document["worker_count"], document["dynamic_fanout"], document["first_sequence"], document["last_sequence"],
		document["preceding_prefix_digest"], document["events_digest"], document["events"], document["terminal_digest"],
		document["terminal_record"], document["safe_to_execute"], document["executes_work"], document["approves_work"],
		document["mutates_repositories"], document["grants_provider_access"], document["publishes_artifacts"],
		document["releases"], document["deploys"], document["advances_authority"],
	}
	calculated, err := canonicalAONextJournalDigest(ordered)
	if err != nil || calculated != prefix.PrefixDigest {
		return fmt.Errorf("AO Next journal prefix digest mismatched")
	}
	return nil
}

func validateAONextJournalLifecycle(events []aoNextJournalEvent) (aoNextJournalLifecycle, error) {
	state := aoNextJournalLifecycle{effects: map[string]bool{}}
	legacyEffects := map[string]struct{}{}
	for _, event := range events {
		if state.terminalState != "" {
			return state, fmt.Errorf("AO Next journal lifecycle is invalid")
		}
		kind := event.Kind
		switch kind.Kind {
		case "provider_request_intent":
			if state.providerStep != 0 || len(state.effects) != 0 || state.verificationSeen || !validJournalDigests(kind.PreparedRunDigest, kind.ExecutionAuthorityDigest) {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			state.providerStep = 1
		case "provider_process_started":
			if state.providerStep != 1 || state.verificationSeen || !validJournalDigests(kind.InvocationDigest) {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			state.providerStep = 2
		case "provider_output_retained":
			if state.providerStep != 2 || state.verificationSeen || !validJournalDigests(kind.RawCaptureDigest) {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			state.providerStep = 3
		case "provider_capture_index_published":
			if state.providerStep != 3 || !validJournalDigests(kind.IndexDigest) {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			state.providerStep = 4
		case "provider_capture_verified":
			if state.providerStep != 4 || !validJournalDigests(kind.IndexDigest) || kind.IndexDigest != events[int(event.Sequence)-1].Kind.IndexDigest {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			state.providerStep = 5
		case "adapter_turn_normalized":
			if state.providerStep != 5 || !validJournalDigests(kind.TurnDigest) {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			state.providerStep = 6
		case "effect_intent":
			if state.verificationSeen || (state.providerStep != 0 && state.providerStep != 6) || strings.TrimSpace(kind.EffectID) == "" || !validJournalDigests(kind.EffectDigest) {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			if _, duplicate := state.effects[kind.EffectID]; duplicate {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			if _, duplicate := legacyEffects[kind.EffectID]; duplicate {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			state.effects[kind.EffectID] = false
		case "effect_committed":
			if state.verificationSeen || (state.providerStep != 0 && state.providerStep != 6) || strings.TrimSpace(kind.EffectID) == "" {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			if _, duplicate := state.effects[kind.EffectID]; duplicate {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			if _, duplicate := legacyEffects[kind.EffectID]; duplicate {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			legacyEffects[kind.EffectID] = struct{}{}
			state.effects[kind.EffectID] = true
		case "effect_completed":
			complete, present := state.effects[kind.Observation.EffectID]
			if !present || complete || state.verificationSeen || kind.Observation.EffectID == "" || !validJournalDigests(kind.Observation.OutputDigest) {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			state.effects[kind.Observation.EffectID] = true
		case "verification_started":
			if (state.providerStep != 0 && state.providerStep != 6) || state.verificationOpen || kind.Attempt == nil || *kind.Attempt != state.verifierRecords {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			for _, complete := range state.effects {
				if !complete {
					return state, fmt.Errorf("AO Next journal lifecycle is invalid")
				}
			}
			state.verificationSeen = true
			state.verificationOpen = true
		case "verifier_recorded":
			if !state.verificationOpen || !validJournalDigests(kind.ReportDigest) {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			state.verificationOpen = false
			state.verifierRecords++
			state.verifierDigest = kind.ReportDigest
		case "terminal_published":
			if state.verificationOpen || state.verifierRecords == 0 || !validJournalDigests(kind.RecordDigest) {
				return state, fmt.Errorf("AO Next journal lifecycle is invalid")
			}
			state.terminalState = "published"
		default:
			return state, fmt.Errorf("AO Next journal lifecycle is invalid")
		}
	}
	return state, nil
}

func validateAONextJournalTerminal(prefix aoNextJournalPrefix, document map[string]any, lifecycle aoNextJournalLifecycle) error {
	if lifecycle.terminalState == "" {
		if prefix.TerminalDigest != nil || prefix.TerminalRecord != nil {
			return fmt.Errorf("AO Next journal terminal record is contradictory")
		}
		return nil
	}
	if prefix.TerminalDigest == nil || prefix.TerminalRecord == nil || len(prefix.Events) == 0 ||
		prefix.Events[len(prefix.Events)-1].Kind.RecordDigest != *prefix.TerminalDigest {
		return fmt.Errorf("AO Next journal terminal record is contradictory")
	}
	terminalDocument, _ := document["terminal_record"].(map[string]any)
	terminalDigest, err := canonicalAONextJournalDigest(terminalDocument)
	if err != nil || terminalDigest != *prefix.TerminalDigest {
		return fmt.Errorf("AO Next journal terminal record is contradictory")
	}
	terminal := prefix.TerminalRecord
	typedMeasurement := terminal.Measurement
	measurement := terminalDocument["measurement"].(map[string]any)
	if terminal.SchemaVersion != "ao.next.live-run-record.v1" || terminal.Variant != "N7" ||
		typedMeasurement.SchemaVersion != "ao.next.run-measurement.v2" ||
		typedMeasurement.Variant != "N7" || typedMeasurement.RunID != prefix.RunID {
		return fmt.Errorf("AO Next journal terminal schema or run identity is unsupported")
	}
	if typedMeasurement.MeasurementOrigin != "synthetic" &&
		typedMeasurement.MeasurementOrigin != "offline_fixture" &&
		typedMeasurement.MeasurementOrigin != "live_provider" {
		return fmt.Errorf("AO Next journal terminal measurement_origin is unsupported")
	}
	if terminal.TerminalState != "passed" && terminal.TerminalState != "failed" && terminal.TerminalState != "denied" && terminal.TerminalState != "interrupted" {
		return fmt.Errorf("AO Next journal terminal state is unsupported")
	}
	for _, field := range []string{"corpus_digest", "raw_capture_digest", "source_digest", "objective_digest", "workspace_seed_digest", "visible_fixtures_digest", "hidden_tests_digest", "verifier_profile_digest", "runtime_digest", "model_digest", "prompt_digest", "policy_digest", "adapter_digest"} {
		if !sha256DigestPattern.MatchString(stringFromAny(measurement[field])) {
			return fmt.Errorf("AO Next journal terminal measurement digest is invalid")
		}
	}
	for _, field := range []string{"run_id", "trial_id", "workspace_instance_id", "task_id", "runtime", "model_identifier", "adapter_version"} {
		if strings.TrimSpace(stringFromAny(measurement[field])) == "" {
			return fmt.Errorf("AO Next journal terminal measurement identity is required")
		}
	}
	if typedMeasurement.WorkerCount != 1 || typedMeasurement.DynamicFanout ||
		typedMeasurement.HiddenTestExposure || typedMeasurement.UnauthorizedEffects != 0 ||
		!typedMeasurement.CrossRuntimeAgreement {
		return fmt.Errorf("AO Next journal terminal violates the one-worker safety boundary")
	}
	taskSuccess := typedMeasurement.TaskSuccess
	if taskSuccess != (terminal.TerminalState == "passed") {
		return fmt.Errorf("AO Next journal terminal state contradicts task success")
	}
	if terminal.TerminalState == "passed" && (!typedMeasurement.EvidenceComplete || !typedMeasurement.EvidenceDigestValid) {
		return fmt.Errorf("passed AO Next journal terminal lacks complete valid evidence")
	}
	if err := validateAONextJournalTokens(measurement["tokens"].(map[string]any)); err != nil {
		return err
	}
	if len(terminal.CaptureDigests) == 0 || !validJournalDigests(append([]string{terminal.RawCaptureIndexDigest, terminal.RecordDigest}, terminal.CaptureDigests...)...) {
		return fmt.Errorf("AO Next journal terminal evidence digest is invalid")
	}
	for _, optional := range []*string{terminal.VerifierReportDigest, terminal.N7ExecutionAuthorityDigest} {
		if optional != nil && !sha256DigestPattern.MatchString(*optional) {
			return fmt.Errorf("AO Next journal terminal optional digest is invalid")
		}
	}
	if terminal.VerifierReportDigest == nil || *terminal.VerifierReportDigest != lifecycle.verifierDigest {
		return fmt.Errorf("AO Next journal terminal verifier digest mismatched")
	}
	workspace := terminalDocument["git_workspace"].(map[string]any)
	if !validJournalDigests(stringFromAny(workspace["control_digest"]), stringFromAny(workspace["index_digest"])) {
		return fmt.Errorf("AO Next journal terminal Git workspace digest is invalid")
	}
	for _, field := range []string{"repository_root", "common_dir", "head_commit", "branch"} {
		if strings.TrimSpace(stringFromAny(workspace[field])) == "" {
			return fmt.Errorf("AO Next journal terminal Git workspace identity is required")
		}
	}
	rawCaptureDigests := typedMeasurement.RawCaptureDigests
	if len(rawCaptureDigests) == 0 || !validJournalDigests(rawCaptureDigests...) {
		return fmt.Errorf("AO Next journal terminal raw capture evidence is invalid")
	}
	rawDigest, err := canonicalAONextJournalDigest(measurement["raw_capture_digests"])
	if err != nil || rawDigest != stringFromAny(measurement["raw_capture_digest"]) || !equalStrings(rawCaptureDigests, terminal.CaptureDigests) {
		return fmt.Errorf("AO Next journal terminal raw capture digest mismatched")
	}
	semanticMeasurement := make(map[string]any, len(measurement)-2)
	for key, value := range measurement {
		if key != "wall_clock_ms" && key != "model_wait_ms" {
			semanticMeasurement[key] = value
		}
	}
	recordDigest, err := canonicalAONextJournalDigest([]any{
		terminalDocument["variant"], terminalDocument["terminal_state"], semanticMeasurement,
		terminalDocument["capture_digests"], terminalDocument["raw_capture_index_digest"],
		terminalDocument["verifier_report_digest"], terminalDocument["n7_execution_authority_digest"],
		terminalDocument["git_workspace"], terminalDocument["ao2_control_diagnostics"], terminalDocument["native_effect_observations"],
	})
	if err != nil || recordDigest != terminal.RecordDigest {
		return fmt.Errorf("AO Next journal terminal record digest mismatched")
	}
	return nil
}

func validateAONextJournalTokens(tokens map[string]any) error {
	fields := []string{"input_tokens", "cached_input_tokens", "reasoning_tokens", "output_tokens"}
	allPresent := true
	total := uint64(0)
	for _, field := range fields {
		if tokens[field] == nil {
			allPresent = false
			continue
		}
		value, ok := strictJSONUnsigned(tokens[field], 64)
		if !ok {
			return fmt.Errorf("AO Next journal token field %q must be a non-negative integer or null", field)
		}
		if math.MaxUint64-total < value {
			return fmt.Errorf("AO Next journal token total overflowed")
		}
		total += value
	}
	reported, ok := strictJSONUnsigned(tokens["reported_total_tokens"], 64)
	if !ok {
		return fmt.Errorf("AO Next journal token reported total must be a non-negative integer")
	}
	if allPresent && total != reported {
		return fmt.Errorf("AO Next journal token reported total %d differs from calculated %d", reported, total)
	}
	return nil
}

func projectAONextJournalPrefix(prefix aoNextJournalPrefix) (string, error) {
	lifecycle, err := validateAONextJournalLifecycle(prefix.Events)
	if err != nil {
		return "", err
	}
	if prefix.TerminalRecord != nil {
		switch prefix.TerminalRecord.TerminalState {
		case "passed", "failed":
			return prefix.TerminalRecord.TerminalState, nil
		case "denied", "interrupted":
			return "stopped", nil
		default:
			return "", fmt.Errorf("AO Next journal terminal state is unsupported")
		}
	}
	if lifecycle.verificationSeen {
		return "verifying", nil
	}
	if len(lifecycle.effects) > 0 {
		for _, complete := range lifecycle.effects {
			if !complete {
				return "effect_outcome_unknown", nil
			}
		}
		return "effects_pending", nil
	}
	switch {
	case lifecycle.providerStep == 6:
		return "provider_captured", nil
	case lifecycle.providerStep >= 2:
		return "provider_outcome_unknown", nil
	case lifecycle.providerStep == 1:
		return "provider_intent_recorded", nil
	default:
		return "prepared", nil
	}
}

func mapKeys(fields map[string]string) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	return keys
}

func strictJSONInteger(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	return parsed, err == nil
}

func strictJSONUnsigned(value any, bits int) (uint64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseUint(number.String(), 10, bits)
	return parsed, err == nil
}

func validJournalDigests(digests ...string) bool {
	for _, digest := range digests {
		if !sha256DigestPattern.MatchString(digest) {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
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

func canonicalAONextJournalDigest(value any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(normalizeAONextJournalNumbers(value)); err != nil {
		return "", err
	}
	return digestBytes(bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))), nil
}

func normalizeAONextJournalNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		return typed
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, child := range typed {
			normalized[key] = normalizeAONextJournalNumbers(child)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, child := range typed {
			normalized[index] = normalizeAONextJournalNumbers(child)
		}
		return normalized
	default:
		return value
	}
}
