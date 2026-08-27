package mission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

func BuildGatewayReplaySuite(readbacks []GatewayReplayReadback, lifecycle *A2ATaskLifecycleReadback, refs []string) GatewayReplaySuiteReadback {
	suite := GatewayReplaySuiteReadback{
		Schema:            "ao.mission.gateway-replay-suite-readback.v0.1",
		Status:            "ready",
		ReplayRefs:        append([]string(nil), refs...),
		Replays:           append([]GatewayReplayReadback(nil), readbacks...),
		A2ALifecycle:      lifecycle,
		MutationAuthority: false,
		ExecutesWork:      false,
		ApprovesWork:      false,
		GeneratedAtUTC:    now(nil),
	}
	for _, readback := range readbacks {
		if suite.CorrelationID == "" && strings.TrimSpace(readback.CorrelationID) != "" {
			suite.CorrelationID = strings.TrimSpace(readback.CorrelationID)
		}
		if strings.HasPrefix(readback.Gateway, "telegram") {
			suite.TelegramReplays++
		}
		if readback.Gateway == "a2a" {
			suite.A2AReplays++
		}
		if readback.Status == "blocked" {
			suite.Status = "blocked"
		}
		suite.Total += readback.Total
		suite.IntentRecorded += readback.IntentRecorded
		suite.Denied += readback.Denied
		suite.Invalid += readback.Invalid
		if readback.MutationAuthority || readback.ExecutesWork || readback.ApprovesWork {
			suite.Status = "blocked"
			suite.MutationAuthority = suite.MutationAuthority || readback.MutationAuthority
			suite.ExecutesWork = suite.ExecutesWork || readback.ExecutesWork
			suite.ApprovesWork = suite.ApprovesWork || readback.ApprovesWork
		}
	}
	if lifecycle != nil {
		suite.A2AReplays++
		suite.Total += lifecycle.Total
		suite.IntentRecorded += lifecycle.IntentRecorded + lifecycle.ResumeRequested + lifecycle.Resumed + lifecycle.CancelRequested + lifecycle.Cancelled
		suite.ArtifactReadbacks += lifecycle.ArtifactReadbacks
		if lifecycle.Status == "blocked" || lifecycle.MutationAuthority || lifecycle.ExecutesWork || lifecycle.ApprovesWork {
			suite.Status = "blocked"
			suite.MutationAuthority = suite.MutationAuthority || lifecycle.MutationAuthority
			suite.ExecutesWork = suite.ExecutesWork || lifecycle.ExecutesWork
			suite.ApprovesWork = suite.ApprovesWork || lifecycle.ApprovesWork
		}
	}
	return suite
}

func BuildGatewayReplayBundleReadback(inputs GatewayReplayBundleInputs) (GatewayReplayBundleReadback, error) {
	readback := GatewayReplayBundleReadback{
		Schema:              "ao.mission.gateway-replay-bundle-readback.v0.1",
		Status:              "ready",
		ReplayRefs:          []string{},
		SafeToExecute:       false,
		ExecutesWork:        false,
		ApprovesWork:        false,
		MutatesRepositories: false,
		ExactNextAction:     "gateway replay bundle verified as local readback only",
		GeneratedAtUTC:      now(nil),
	}
	var allowedChats map[string]string
	if strings.TrimSpace(inputs.TelegramMatrixPath) != "" || strings.TrimSpace(inputs.TelegramUpdatesPath) != "" || strings.TrimSpace(inputs.TelegramWebhookPath) != "" {
		if strings.TrimSpace(inputs.TelegramConfigPath) == "" {
			return GatewayReplayBundleReadback{}, fmt.Errorf("gateway replay-bundle requires Telegram config with Telegram fixtures")
		}
		cfg, err := LoadTelegramConfig(inputs.TelegramConfigPath)
		if err != nil {
			return GatewayReplayBundleReadback{}, err
		}
		allowedChats = cfg.AllowedChats
	}
	if strings.TrimSpace(inputs.TelegramMatrixPath) != "" {
		replay, err := ReplayTelegramCommandMatrix(inputs.TelegramMatrixPath, allowedChats)
		if err != nil {
			return GatewayReplayBundleReadback{}, err
		}
		applyGatewayReplayToBundle(&readback, replay)
		readback.TelegramReadbacks++
		readback.ReplayRefs = append(readback.ReplayRefs, filepathSlash(inputs.TelegramMatrixPath))
	}
	if strings.TrimSpace(inputs.TelegramUpdatesPath) != "" {
		replay, err := ReplayTelegramUpdates(inputs.TelegramUpdatesPath, allowedChats)
		if err != nil {
			return GatewayReplayBundleReadback{}, err
		}
		applyGatewayReplayToBundle(&readback, replay)
		readback.TelegramReadbacks++
		readback.ReplayRefs = append(readback.ReplayRefs, filepathSlash(inputs.TelegramUpdatesPath))
	}
	if strings.TrimSpace(inputs.TelegramWebhookPath) != "" {
		replay, err := ReplayTelegramWebhookFixture(inputs.TelegramWebhookPath, allowedChats)
		if err != nil {
			return GatewayReplayBundleReadback{}, err
		}
		applyGatewayReplayToBundle(&readback, replay)
		readback.TelegramReadbacks++
		readback.ReplayRefs = append(readback.ReplayRefs, filepathSlash(inputs.TelegramWebhookPath))
	}
	if strings.TrimSpace(inputs.A2AHTTPPath) != "" {
		replay, err := ReplayA2AHTTPFixture(inputs.A2AHTTPPath)
		if err != nil {
			return GatewayReplayBundleReadback{}, err
		}
		applyGatewayReplayToBundle(&readback, replay)
		readback.A2AReadbacks++
		readback.ReplayRefs = append(readback.ReplayRefs, filepathSlash(inputs.A2AHTTPPath))
	}
	if strings.TrimSpace(inputs.A2ALifecyclePath) != "" {
		replay, err := ReplayA2ATaskLifecycle(inputs.A2ALifecyclePath)
		if err != nil {
			return GatewayReplayBundleReadback{}, err
		}
		readback.TotalIntents += replay.IntentRecorded + replay.ResumeRequested + replay.Resumed + replay.CancelRequested + replay.Cancelled
		readback.A2AReadbacks++
		readback.ReplayRefs = append(readback.ReplayRefs, filepathSlash(inputs.A2ALifecyclePath))
		if replay.Status == "blocked" || replay.MutationAuthority || replay.ExecutesWork || replay.ApprovesWork {
			readback.Status = "blocked"
		}
	}
	if strings.TrimSpace(inputs.SchedulerPath) != "" {
		replay, err := ReplaySchedulerReadbacks(inputs.SchedulerPath)
		if err != nil {
			return GatewayReplayBundleReadback{}, err
		}
		readback.SchedulerReadbacks++
		readback.TotalIntents += replay.Total
		readback.ReplayRefs = append(readback.ReplayRefs, filepathSlash(inputs.SchedulerPath))
		if replay.Status == "blocked" || replay.ExecutesWork || replay.ApprovesWork {
			readback.Status = "blocked"
		}
	}
	if len(readback.ReplayRefs) == 0 {
		readback.Status = "blocked"
		readback.ExactNextAction = "provide at least one replay fixture"
	}
	return readback, nil
}

func applyGatewayReplayToBundle(bundle *GatewayReplayBundleReadback, replay GatewayReplayReadback) {
	bundle.TotalIntents += replay.IntentRecorded
	bundle.Denied += replay.Denied
	bundle.Invalid += replay.Invalid
	if replay.Status == "blocked" || replay.MutationAuthority || replay.ExecutesWork || replay.ApprovesWork {
		bundle.Status = "blocked"
	}
}

func filepathSlash(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

func BuildA2ACompatibilityReadback(agentCardPath, httpFixturePath, lifecyclePath string) (A2ACompatibilityReadback, error) {
	var card A2AAgentCard
	body, err := os.ReadFile(agentCardPath)
	if err != nil {
		return A2ACompatibilityReadback{}, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		return A2ACompatibilityReadback{}, err
	}
	if err := validateNoDuplicateJSONKeys(body); err != nil {
		return A2ACompatibilityReadback{}, err
	}
	if err := json.Unmarshal(body, &card); err != nil {
		return A2ACompatibilityReadback{}, err
	}
	if card.Schema != A2AAgentCardSchema {
		return A2ACompatibilityReadback{}, fmt.Errorf("agent card schema must be %s", A2AAgentCardSchema)
	}
	if card.MutationAuthority || card.CapabilitiesDetail["streaming"] || card.CapabilitiesDetail["push_notifications"] {
		return A2ACompatibilityReadback{}, fmt.Errorf("agent card must remain readback-only")
	}
	if len(card.Skills) == 0 || !card.CapabilitiesDetail["artifact_readbacks"] {
		return A2ACompatibilityReadback{}, fmt.Errorf("agent card must expose readback skills and artifact readbacks")
	}
	httpReadback, err := ReplayA2AHTTPFixture(httpFixturePath)
	if err != nil {
		return A2ACompatibilityReadback{}, err
	}
	lifecycle, err := ReplayA2ATaskLifecycle(lifecyclePath)
	if err != nil {
		return A2ACompatibilityReadback{}, err
	}
	status := "ready"
	if httpReadback.Status != "ready" || lifecycle.Status != "ready" {
		status = "blocked"
	}
	return A2ACompatibilityReadback{
		Schema:            "ao.mission.a2a-compatibility-readback.v0.1",
		Status:            status,
		ProtocolVersion:   card.ProtocolVersion,
		AgentCardSkills:   len(card.Skills),
		Methods:           len(card.Methods),
		HTTPRequests:      httpReadback.Total,
		LifecycleTasks:    lifecycle.Total,
		ArtifactReadbacks: lifecycle.ArtifactReadbacks,
		MutationAuthority: false,
		ExecutesWork:      false,
		ApprovesWork:      false,
		GeneratedAtUTC:    now(nil),
	}, nil
}

func BuildA2AStreamingDenialReadback(agentCardPath string) (A2AStreamingDenialReadback, error) {
	var card A2AAgentCard
	body, err := os.ReadFile(agentCardPath)
	if err != nil {
		return A2AStreamingDenialReadback{}, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		return A2AStreamingDenialReadback{}, err
	}
	if err := validateNoDuplicateJSONKeys(body); err != nil {
		return A2AStreamingDenialReadback{}, err
	}
	if err := json.Unmarshal(body, &card); err != nil {
		return A2AStreamingDenialReadback{}, err
	}
	sse := card.CapabilitiesDetail["sse"]
	streaming := card.CapabilitiesDetail["streaming"] || sse || stringSliceContains(card.Capabilities, "sse=true")
	push := card.CapabilitiesDetail["push_notifications"]
	denied := "none"
	status := "ready"
	if streaming || push {
		denied = "streaming_or_push"
		status = "denied"
	}
	return A2AStreamingDenialReadback{
		Schema:             "ao.mission.a2a-streaming-denial-readback.v0.1",
		Status:             status,
		StreamingRequested: streaming,
		SSERequested:       sse,
		PushRequested:      push,
		DeniedCapability:   denied,
		MutationAuthority:  false,
		ExecutesWork:       false,
		ApprovesWork:       false,
		ExactNextAction:    "keep A2A gateway in readback-only non-streaming mode",
		GeneratedAtUTC:     now(nil),
	}, nil
}

func BuildA2ACancellationReplayReadback(lifecyclePath string) (A2ACancellationReplayReadback, error) {
	lifecycle, err := ReplayA2ATaskLifecycle(lifecyclePath)
	if err != nil {
		return A2ACancellationReplayReadback{}, err
	}
	status := "ready"
	if lifecycle.CancelRequested == 0 || lifecycle.Cancelled == 0 || lifecycle.MutationAuthority || lifecycle.ExecutesWork || lifecycle.ApprovesWork {
		status = "blocked"
	}
	return A2ACancellationReplayReadback{
		Schema:            "ao.mission.a2a-cancellation-replay-readback.v0.1",
		Status:            status,
		Total:             lifecycle.Total,
		CancelRequested:   lifecycle.CancelRequested,
		Cancelled:         lifecycle.Cancelled,
		MutationAuthority: false,
		ExecutesWork:      false,
		ApprovesWork:      false,
		ExactNextAction:   "record A2A cancellation as readback only; route any continuation through AO Mission gates",
		GeneratedAtUTC:    now(nil),
	}, nil
}

func DiffGovernanceSnapshots(before, after GovernanceSnapshot) GovernanceSnapshotDiff {
	fields := []string{}
	if before.MissionID != after.MissionID {
		fields = append(fields, "mission_id")
	}
	if before.CurrentRoute != after.CurrentRoute {
		fields = append(fields, "current_route")
	}
	if before.CurrentPhase != after.CurrentPhase {
		fields = append(fields, "current_phase")
	}
	if before.ExactNextAction != after.ExactNextAction {
		fields = append(fields, "exact_next_action")
	}
	if before.EvidenceFreshnessStatus != after.EvidenceFreshnessStatus {
		fields = append(fields, "evidence_freshness_status")
	}
	status := "unchanged"
	if len(fields) > 0 {
		status = "changed"
	}
	return GovernanceSnapshotDiff{
		Schema:         "ao.mission.governance-snapshot-diff.v0.1",
		Status:         status,
		MissionID:      after.MissionID,
		ChangedFields:  len(fields),
		Fields:         fields,
		SafeToExecute:  false,
		ExecutesWork:   false,
		ApprovesWork:   false,
		GeneratedAtUTC: now(nil),
	}
}

func BuildTelegramRoleMatrix(cfg TelegramConfig) TelegramRoleMatrixReadback {
	entries := make([]TelegramRoleEntry, 0, len(cfg.AllowedChats))
	for chatID, role := range cfg.AllowedChats {
		entries = append(entries, TelegramRoleEntry{ChatID: chatID, Role: role})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ChatID < entries[j].ChatID })
	readback := TelegramRoleMatrixReadback{
		Schema:            "ao.mission.telegram-role-matrix-readback.v0.1",
		Status:            "ready",
		ChatCount:         len(entries),
		Roles:             entries,
		MutationAuthority: false,
		ExecutesWork:      false,
		ApprovesWork:      false,
		GeneratedAtUTC:    now(nil),
	}
	for _, entry := range entries {
		switch entry.Role {
		case "admin":
			readback.AdminCount++
		case "user":
			readback.UserCount++
		}
	}
	return readback
}

func LoadGovernanceSnapshot(path string) (GovernanceSnapshot, error) {
	var snapshot GovernanceSnapshot
	body, err := os.ReadFile(path)
	if err != nil {
		return snapshot, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		return snapshot, err
	}
	if err := validateNoDuplicateJSONKeys(body); err != nil {
		return snapshot, err
	}
	return snapshot, json.Unmarshal(body, &snapshot)
}

func BuildMissionArchive(record Record) (MissionArchive, error) {
	if err := validateRecordWorkflowContract(record); err != nil {
		return MissionArchive{}, err
	}
	archivedRecord, redactions := publicSafeArchiveRecord(record)
	archive := MissionArchive{
		Schema:                "ao.mission.archive.v0.1",
		MissionID:             archivedRecord.MissionID,
		Record:                archivedRecord,
		Snapshot:              Snapshot(archivedRecord),
		FinalRollup:           BuildFinalRollup(archivedRecord),
		ArtifactCount:         len(archivedRecord.ArtifactRefs),
		SourceObjectiveDigest: record.ObjectiveDigest,
		PublicSafeRedactions:  redactions,
		SafeToExecute:         false,
		ExecutesWork:          false,
		ApprovesWork:          false,
		GeneratedAtUTC:        now(nil),
	}
	body, err := json.Marshal(archive)
	if err != nil {
		return MissionArchive{}, err
	}
	sum := sha256.Sum256(body)
	archive.ArchiveDigest = "sha256:" + hex.EncodeToString(sum[:])
	return archive, nil
}

func publicSafeArchiveRecord(record Record) (Record, []string) {
	body, _ := json.Marshal(record)
	var value any
	_ = json.Unmarshal(body, &value)
	value, redactions := redactArchiveLocalPathsValue(value, "record", nil)
	body, _ = json.Marshal(value)
	var archived Record
	_ = json.Unmarshal(body, &archived)
	bindArchiveCorrelationLocators(record, &archived)
	archived.ObjectiveDigest = record.ObjectiveDigest
	archived.ObjectiveRedacted = record.ObjectiveRedacted || archived.Objective != record.Objective
	uniqueRedactions := []string{}
	for _, redaction := range redactions {
		uniqueRedactions = appendMissingString(uniqueRedactions, redaction)
	}
	return archived, uniqueRedactions
}

var archiveLocalPathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(^|[^[:alnum:]_/\\])[a-z]:[\\/][^\s"',;)}\]]*`),
	regexp.MustCompile(`(^|[^[:alnum:]_/\\])\\\\[^\\/\s]+[\\/][^\s"',;)}\]]*`),
}

var archivePOSIXPathPattern = regexp.MustCompile(
	`(^|[^[:alnum:]_/])((//(?:\[[^]]+\]|[^/\s"',;)}\]]+)(?:/[^\s"',;)}\]]*)?)|(/[^\s"',;)}\]]*))`,
)

func redactArchiveLocalPathsValue(value any, path string, redactions []string) (any, []string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			typed[key], redactions = redactArchiveLocalPathsValue(child, path+"."+key, redactions)
		}
		return typed, redactions
	case []any:
		for i, child := range typed {
			typed[i], redactions = redactArchiveLocalPathsValue(child, fmt.Sprintf("%s[%d]", path, i), redactions)
		}
		return typed, redactions
	case string:
		if isCorrelationProvenancePathField(path) && validCorrelationJSONPointer(typed) {
			return typed, redactions
		}
		redacted := typed
		for _, pattern := range archiveLocalPathPatterns {
			redacted = pattern.ReplaceAllString(
				redacted,
				"${1}"+correlationRedactedPathSentinel,
			)
		}
		redacted = archivePOSIXPathPattern.ReplaceAllStringFunc(redacted, func(match string) string {
			prefixLength := 0
			if match[0] != '/' {
				prefixLength = 1
			}
			prefix, candidate := match[:prefixLength], match[prefixLength:]
			if isProtocolRelativeReference(candidate) {
				return match
			}
			return prefix + correlationRedactedPathSentinel
		})
		if redacted == typed {
			return typed, redactions
		}
		return redacted, append(redactions, path+" local path redacted")
	default:
		return value, redactions
	}
}

func isCorrelationProvenancePathField(path string) bool {
	return strings.HasSuffix(path, ".native_field") ||
		strings.HasSuffix(path, ".parent_digest_field")
}

func isProtocolRelativeReference(candidate string) bool {
	if !strings.HasPrefix(candidate, "//") {
		return false
	}
	parsed, err := url.Parse(candidate)
	return err == nil &&
		parsed.Scheme == "" &&
		parsed.Host != "" &&
		parsed.User == nil
}

func LoadMissionArchive(path string) (MissionArchive, error) {
	var archive MissionArchive
	body, err := os.ReadFile(path)
	if err != nil {
		return archive, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		return archive, err
	}
	if err := json.Unmarshal(body, &archive); err != nil {
		return archive, err
	}
	if archive.Schema != "ao.mission.archive.v0.1" {
		return archive, fmt.Errorf("mission archive schema must be ao.mission.archive.v0.1")
	}
	if archive.SafeToExecute || archive.ExecutesWork || archive.ApprovesWork {
		return archive, fmt.Errorf("mission archive must not claim execution or approval authority")
	}
	if archive.Record.Schema != RecordSchema || archive.Record.MissionID != archive.MissionID {
		return archive, fmt.Errorf("mission archive record does not match archive mission_id")
	}
	if err := validateRecordWorkflowContract(archive.Record); err != nil {
		return archive, err
	}
	return archive, nil
}

func ValidateMissionArchive(path string) (MissionArchiveValidation, error) {
	archive, err := LoadMissionArchive(path)
	if err != nil {
		return MissionArchiveValidation{}, err
	}
	expected := archive.ArchiveDigest
	archive.ArchiveDigest = ""
	body, err := json.Marshal(archive)
	if err != nil {
		return MissionArchiveValidation{}, err
	}
	sum := sha256.Sum256(body)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if expected == "" || actual != expected {
		return MissionArchiveValidation{}, fmt.Errorf("mission archive digest mismatch")
	}
	if archive.Record.Schema != RecordSchema || archive.Record.MissionID != archive.MissionID {
		return MissionArchiveValidation{}, fmt.Errorf("mission archive record does not match archive mission_id")
	}
	if err := validateRecordWorkflowContract(archive.Record); err != nil {
		return MissionArchiveValidation{}, err
	}
	return MissionArchiveValidation{
		Schema:         "ao.mission.archive-validation.v0.1",
		Status:         "ready",
		MissionID:      archive.MissionID,
		ArchiveDigest:  expected,
		ArtifactCount:  archive.ArtifactCount,
		SafeToExecute:  false,
		ExecutesWork:   false,
		ApprovesWork:   false,
		GeneratedAtUTC: now(nil),
	}, nil
}

func ImportMissionArchive(store Store, path string) (MissionArchiveImportReadback, error) {
	validation, err := ValidateMissionArchive(path)
	if err != nil {
		return MissionArchiveImportReadback{}, err
	}
	archive, err := LoadMissionArchive(path)
	if err != nil {
		return MissionArchiveImportReadback{}, err
	}
	if archive.Record.Schema != RecordSchema || archive.Record.MissionID != archive.MissionID {
		return MissionArchiveImportReadback{}, fmt.Errorf("mission archive record does not match archive mission_id")
	}
	if archive.SourceObjectiveDigest != archive.Record.ObjectiveDigest {
		return MissionArchiveImportReadback{}, fmt.Errorf("mission archive source objective digest does not match record")
	}
	if err := validateRecordWorkflowContract(archive.Record); err != nil {
		return MissionArchiveImportReadback{}, err
	}
	if err := store.Save(archive.Record); err != nil {
		return MissionArchiveImportReadback{}, err
	}
	return MissionArchiveImportReadback{
		Schema:         "ao.mission.archive-import-readback.v0.1",
		Status:         "ready",
		MissionID:      archive.MissionID,
		ArchiveDigest:  validation.ArchiveDigest,
		SafeToExecute:  false,
		ExecutesWork:   false,
		ApprovesWork:   false,
		GeneratedAtUTC: now(nil),
	}, nil
}

func BuildGatewayReadinessRollup(paths ...string) (GatewayReadinessRollup, error) {
	return BuildGatewayReadinessRollupWithCorrelation("", paths...)
}

func BuildGatewayReadinessRollupWithCorrelation(correlationID string, paths ...string) (GatewayReadinessRollup, error) {
	return BuildGatewayReadinessRollupWithMissionAndCorrelation("", correlationID, paths...)
}

func BuildGatewayReadinessRollupWithMissionAndCorrelation(missionID, correlationID string, paths ...string) (GatewayReadinessRollup, error) {
	rollup := GatewayReadinessRollup{
		Schema:              "ao.mission.gateway-readiness-rollup.v0.1",
		MissionID:           strings.TrimSpace(missionID),
		Status:              "ready",
		CorrelationID:       strings.TrimSpace(correlationID),
		ReadbackRefs:        []string{},
		SafeToExecute:       false,
		ExecutesWork:        false,
		ApprovesWork:        false,
		MutatesRepositories: false,
		ExactNextAction:     "route ready gateway readbacks through AO Mission, Atlas, Foundry, and Command gates",
		GeneratedAtUTC:      now(nil),
	}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return GatewayReadinessRollup{}, err
		}
		if err := ValidatePublicSafeText(string(body)); err != nil {
			return GatewayReadinessRollup{}, err
		}
		if err := validateNoDuplicateJSONKeys(body); err != nil {
			return GatewayReadinessRollup{}, err
		}
		var packet map[string]any
		if err := json.Unmarshal(body, &packet); err != nil {
			return GatewayReadinessRollup{}, err
		}
		if rollup.CorrelationID == "" {
			if value, ok := packet["correlation_id"].(string); ok {
				rollup.CorrelationID = strings.TrimSpace(value)
			}
		}
		rollup.ReadbackCount++
		rollup.ReadbackRefs = append(rollup.ReadbackRefs, path)
		if packet["status"] == "blocked" || packet["status"] == "denied" || packet["safe_to_execute"] == true || packet["executes_work"] == true || packet["approves_work"] == true || packet["mutation_authority"] == true {
			rollup.BlockedReadbacks++
			rollup.Status = "blocked"
		}
	}
	if rollup.ReadbackCount == 0 {
		return GatewayReadinessRollup{}, fmt.Errorf("gateway readiness rollup requires at least one readback")
	}
	return rollup, nil
}
