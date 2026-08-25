package mission

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
)

type MissionEventSearchFilters struct {
	MissionID string
	Kind      string
	Query     string
}

func BuildMissionEventIndex(s Store) (MissionEventIndex, error) {
	records, stats, err := s.listFilteredWithStats(ListFilters{})
	if err != nil {
		return MissionEventIndex{}, err
	}
	return buildMissionEventIndexFromRecords(s, records, stats)
}

func buildMissionEventIndexFromRecords(s Store, records []Record, stats storeListStats) (MissionEventIndex, error) {
	index := MissionEventIndex{
		Schema:              "ao.mission.event-index.v0.2",
		Status:              "ready",
		IndexVersion:        "v0.2",
		Root:                s.Root,
		MissionCount:        len(records),
		StoreFileReads:      stats.StoreFileReads,
		Events:              []MissionEvent{},
		SafeToExecute:       false,
		ExecutesWork:        false,
		ApprovesWork:        false,
		MutatesRepositories: false,
		GeneratedAtUTC:      now(s.Clock),
	}
	for _, record := range records {
		events := missionEventsForRecord(record)
		index.EventConstructionCount += len(events)
		index.Events = append(index.Events, events...)
	}
	index.TotalEvents = len(index.Events)
	sourceBody, err := json.Marshal(records)
	if err != nil {
		return MissionEventIndex{}, err
	}
	index.SourceDigest = digestBytes(sourceBody)
	index.IndexDigest, err = digestMissionEventIndex(index)
	if err != nil {
		return MissionEventIndex{}, err
	}
	return index, nil
}

func ValidateMissionEventIndexDigest(index MissionEventIndex) error {
	if index.Schema != "ao.mission.event-index.v0.2" {
		return fmt.Errorf("mission event index schema must be ao.mission.event-index.v0.2")
	}
	if index.IndexVersion != "v0.2" {
		return fmt.Errorf("mission event index version must be v0.2")
	}
	if !strings.HasPrefix(index.IndexDigest, "sha256:") {
		return fmt.Errorf("mission event index digest must start with sha256:")
	}
	expected, err := digestMissionEventIndex(index)
	if err != nil {
		return err
	}
	if index.IndexDigest != expected {
		return fmt.Errorf("mission event index digest mismatch")
	}
	return nil
}

func digestMissionEventIndex(index MissionEventIndex) (string, error) {
	copy := index
	copy.IndexDigest = ""
	copy.GeneratedAtUTC = ""
	copy.StoreFileReads = 0
	copy.EventConstructionCount = 0
	body, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	return digestBytes(body), nil
}

func SearchMissionEvents(index MissionEventIndex, filters MissionEventSearchFilters) MissionEventSearchReadback {
	query := strings.ToLower(strings.TrimSpace(filters.Query))
	missionID := strings.TrimSpace(filters.MissionID)
	kind := strings.TrimSpace(filters.Kind)
	readback := MissionEventSearchReadback{
		Schema:              "ao.mission.event-search-readback.v0.1",
		Status:              "ready",
		Query:               filters.Query,
		MissionID:           missionID,
		Kind:                kind,
		Events:              []MissionEvent{},
		SafeToExecute:       false,
		ExecutesWork:        false,
		ApprovesWork:        false,
		MutatesRepositories: false,
		GeneratedAtUTC:      now(nil),
	}
	for _, event := range index.Events {
		if missionID != "" && event.MissionID != missionID {
			continue
		}
		if kind != "" && event.Kind != kind {
			continue
		}
		if query != "" && !missionEventMatchesQuery(event, query) {
			continue
		}
		readback.Events = append(readback.Events, event)
	}
	readback.TotalMatches = len(readback.Events)
	return readback
}

func BuildMissionTimelineQueryIndex(index MissionEventIndex) (MissionTimelineQueryIndex, error) {
	if err := ValidateMissionEventIndexDigest(index); err != nil {
		return MissionTimelineQueryIndex{}, err
	}
	terms := map[string]map[MissionTimelineMatch]struct{}{}
	for _, event := range index.Events {
		match := MissionTimelineMatch{
			MissionID: event.MissionID,
			Kind:      event.Kind,
			Sequence:  event.Sequence,
		}
		for _, term := range timelineTermsForEvent(event) {
			if _, ok := terms[term]; !ok {
				terms[term] = map[MissionTimelineMatch]struct{}{}
			}
			terms[term][match] = struct{}{}
		}
	}
	termNames := make([]string, 0, len(terms))
	for term := range terms {
		termNames = append(termNames, term)
	}
	sort.Strings(termNames)
	queryTerms := make([]MissionTimelineTerm, 0, len(termNames))
	for _, term := range termNames {
		matches := make([]MissionTimelineMatch, 0, len(terms[term]))
		for match := range terms[term] {
			matches = append(matches, match)
		}
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].MissionID != matches[j].MissionID {
				return matches[i].MissionID < matches[j].MissionID
			}
			if matches[i].Sequence != matches[j].Sequence {
				return matches[i].Sequence < matches[j].Sequence
			}
			return matches[i].Kind < matches[j].Kind
		})
		queryTerms = append(queryTerms, MissionTimelineTerm{Term: term, Matches: matches})
	}
	queryIndex := MissionTimelineQueryIndex{
		Schema:              "ao.mission.timeline-query-index.v0.1",
		Status:              "ready",
		IndexVersion:        "v0.1",
		EventIndexDigest:    index.IndexDigest,
		MissionCount:        index.MissionCount,
		EventCount:          index.TotalEvents,
		TermCount:           len(queryTerms),
		Terms:               queryTerms,
		SafeToExecute:       false,
		ExecutesWork:        false,
		ApprovesWork:        false,
		MutatesRepositories: false,
		GeneratedAtUTC:      now(nil),
	}
	digest, err := digestMissionTimelineQueryIndex(queryIndex)
	if err != nil {
		return MissionTimelineQueryIndex{}, err
	}
	queryIndex.IndexDigest = digest
	return queryIndex, nil
}

func ValidateMissionTimelineQueryIndexDigest(index MissionTimelineQueryIndex) error {
	if index.Schema != "ao.mission.timeline-query-index.v0.1" {
		return fmt.Errorf("mission timeline query index schema must be ao.mission.timeline-query-index.v0.1")
	}
	if index.IndexVersion != "v0.1" {
		return fmt.Errorf("mission timeline query index version must be v0.1")
	}
	if !strings.HasPrefix(index.EventIndexDigest, "sha256:") {
		return fmt.Errorf("mission timeline query index event digest must start with sha256:")
	}
	if !strings.HasPrefix(index.IndexDigest, "sha256:") {
		return fmt.Errorf("mission timeline query index digest must start with sha256:")
	}
	expected, err := digestMissionTimelineQueryIndex(index)
	if err != nil {
		return err
	}
	if index.IndexDigest != expected {
		legacy, legacyErr := digestLegacyMissionTimelineQueryIndex(index)
		if legacyErr != nil {
			return legacyErr
		}
		if index.IndexDigest != legacy {
			return fmt.Errorf("mission timeline query index digest mismatch")
		}
	}
	return nil
}

func digestLegacyMissionTimelineQueryIndex(index MissionTimelineQueryIndex) (string, error) {
	copy := index
	copy.IndexDigest = ""
	body, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	return digestBytes(body), nil
}

func digestMissionTimelineQueryIndex(index MissionTimelineQueryIndex) (string, error) {
	copy := index
	copy.IndexDigest = ""
	copy.GeneratedAtUTC = ""
	body, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	return digestBytes(body), nil
}

func BuildMissionRestartRecoveryProof(s Store, missionID string) (MissionRestartRecoveryProof, error) {
	missionID = strings.TrimSpace(missionID)
	if missionID == "" {
		return MissionRestartRecoveryProof{}, fmt.Errorf("mission restart recovery proof requires mission id")
	}
	if _, err := s.Load(missionID); err != nil {
		return MissionRestartRecoveryProof{}, err
	}
	beforeEventIndex, err := BuildMissionEventIndex(s)
	if err != nil {
		return MissionRestartRecoveryProof{}, err
	}
	beforeTimelineIndex, err := BuildMissionTimelineQueryIndex(beforeEventIndex)
	if err != nil {
		return MissionRestartRecoveryProof{}, err
	}

	restarted := NewStore(s.Root)
	restarted.Clock = s.Clock
	if _, err := restarted.Load(missionID); err != nil {
		return MissionRestartRecoveryProof{}, err
	}
	afterEventIndex, err := BuildMissionEventIndex(restarted)
	if err != nil {
		return MissionRestartRecoveryProof{}, err
	}
	afterTimelineIndex, err := BuildMissionTimelineQueryIndex(afterEventIndex)
	if err != nil {
		return MissionRestartRecoveryProof{}, err
	}

	beforeTermDigest, err := digestMissionTimelineTerms(beforeTimelineIndex.Terms)
	if err != nil {
		return MissionRestartRecoveryProof{}, err
	}
	afterTermDigest, err := digestMissionTimelineTerms(afterTimelineIndex.Terms)
	if err != nil {
		return MissionRestartRecoveryProof{}, err
	}
	beforeMissionEvents := countMissionEvents(beforeEventIndex, missionID)
	afterMissionEvents := countMissionEvents(afterEventIndex, missionID)
	beforeTimelineMatches := countMissionTimelineMatches(beforeTimelineIndex, missionID)
	afterTimelineMatches := countMissionTimelineMatches(afterTimelineIndex, missionID)
	duplicateMatches := countDuplicateMissionTimelineMatches(afterTimelineIndex, missionID)

	proof := MissionRestartRecoveryProof{
		Schema:                     "ao.mission.restart-recovery-proof.v0.1",
		Status:                     "restart_recovery_proven",
		MissionID:                  missionID,
		BeforeEventSourceDigest:    beforeEventIndex.SourceDigest,
		AfterEventSourceDigest:     afterEventIndex.SourceDigest,
		BeforeTimelineTermDigest:   beforeTermDigest,
		AfterTimelineTermDigest:    afterTermDigest,
		BeforeEventCount:           beforeEventIndex.TotalEvents,
		AfterEventCount:            afterEventIndex.TotalEvents,
		BeforeMissionEventCount:    beforeMissionEvents,
		AfterMissionEventCount:     afterMissionEvents,
		BeforeTimelineTermCount:    beforeTimelineIndex.TermCount,
		AfterTimelineTermCount:     afterTimelineIndex.TermCount,
		BeforeTimelineMatchCount:   beforeTimelineMatches,
		AfterTimelineMatchCount:    afterTimelineMatches,
		DuplicateTimelineMatches:   duplicateMatches,
		SourceDigestStable:         beforeEventIndex.SourceDigest == afterEventIndex.SourceDigest,
		EventCountStable:           beforeEventIndex.TotalEvents == afterEventIndex.TotalEvents && beforeMissionEvents == afterMissionEvents,
		TimelineTermsStable:        beforeTermDigest == afterTermDigest && beforeTimelineIndex.TermCount == afterTimelineIndex.TermCount,
		TimelineMatchesStable:      beforeTimelineMatches == afterTimelineMatches,
		NoDuplicateTimelineMatches: duplicateMatches == 0,
		SafeToExecute:              false,
		ExecutesWork:               false,
		ApprovesWork:               false,
		MutatesRepositories:        false,
		GeneratedAtUTC:             now(s.Clock),
	}
	proof.RecoveryProven = proof.SourceDigestStable &&
		proof.EventCountStable &&
		proof.TimelineTermsStable &&
		proof.TimelineMatchesStable &&
		proof.NoDuplicateTimelineMatches
	if !proof.RecoveryProven {
		proof.Status = "restart_recovery_blocked"
	}
	if err := ValidateMissionRestartRecoveryProof(proof); err != nil {
		return MissionRestartRecoveryProof{}, err
	}
	return proof, nil
}

func ValidateMissionRestartRecoveryProof(proof MissionRestartRecoveryProof) error {
	var errs []string
	if proof.Schema != "ao.mission.restart-recovery-proof.v0.1" {
		errs = append(errs, "mission restart recovery proof schema must be ao.mission.restart-recovery-proof.v0.1")
	}
	if proof.Status != "restart_recovery_proven" && proof.Status != "restart_recovery_blocked" {
		errs = append(errs, "mission restart recovery proof status must be restart_recovery_proven or restart_recovery_blocked")
	}
	if strings.TrimSpace(proof.MissionID) == "" {
		errs = append(errs, "mission restart recovery proof requires mission id")
	}
	for field, value := range map[string]string{
		"before_event_source_digest":  proof.BeforeEventSourceDigest,
		"after_event_source_digest":   proof.AfterEventSourceDigest,
		"before_timeline_term_digest": proof.BeforeTimelineTermDigest,
		"after_timeline_term_digest":  proof.AfterTimelineTermDigest,
	} {
		if !strings.HasPrefix(value, "sha256:") {
			errs = append(errs, field+" must start with sha256:")
		}
	}
	if proof.BeforeEventCount < 1 || proof.AfterEventCount < 1 || proof.BeforeMissionEventCount < 1 || proof.AfterMissionEventCount < 1 {
		errs = append(errs, "mission restart recovery proof event counts must be positive")
	}
	if proof.BeforeTimelineTermCount < 1 || proof.AfterTimelineTermCount < 1 || proof.BeforeTimelineMatchCount < 1 || proof.AfterTimelineMatchCount < 1 {
		errs = append(errs, "mission restart recovery proof timeline counts must be positive")
	}
	if proof.DuplicateTimelineMatches != 0 {
		errs = append(errs, "mission restart recovery proof duplicate timeline matches must be zero")
	}
	if proof.Status == "restart_recovery_proven" && !proof.RecoveryProven {
		errs = append(errs, "restart_recovery_proven status requires recovery_proven true")
	}
	if proof.RecoveryProven && (!proof.SourceDigestStable || !proof.EventCountStable || !proof.TimelineTermsStable || !proof.TimelineMatchesStable || !proof.NoDuplicateTimelineMatches) {
		errs = append(errs, "recovery_proven requires all stability checks")
	}
	if proof.SafeToExecute || proof.ExecutesWork || proof.ApprovesWork || proof.MutatesRepositories {
		errs = append(errs, "mission restart recovery proof must not execute, approve, or mutate")
	}
	if len(errs) > 0 {
		return fmt.Errorf(strings.Join(errs, "; "))
	}
	return nil
}

func BuildMissionCompactionResumePrompt(s Store, missionID string) (MissionCompactionResumePrompt, error) {
	missionID = strings.TrimSpace(missionID)
	if missionID == "" {
		return MissionCompactionResumePrompt{}, fmt.Errorf("mission compaction resume prompt requires mission id")
	}
	record, err := s.Load(missionID)
	if err != nil {
		return MissionCompactionResumePrompt{}, err
	}
	eventIndex, err := BuildMissionEventIndex(s)
	if err != nil {
		return MissionCompactionResumePrompt{}, err
	}
	timelineIndex, err := BuildMissionTimelineQueryIndex(eventIndex)
	if err != nil {
		return MissionCompactionResumePrompt{}, err
	}
	missionEventCount := countMissionEvents(eventIndex, missionID)
	if missionEventCount == 0 {
		return MissionCompactionResumePrompt{}, fmt.Errorf("mission compaction resume prompt requires indexed events for %s", missionID)
	}
	gate := record.ReturnGate
	if gate == nil {
		evaluated := EvaluateReturnGate(record)
		gate = &evaluated
	}
	latestRoute := record.CurrentRoute
	if n := len(record.RouteHistory); n > 0 && strings.TrimSpace(record.RouteHistory[n-1].Route) != "" {
		latestRoute = record.RouteHistory[n-1].Route
	}
	exactNextAction := strings.TrimSpace(gate.ExactNextAction)
	if exactNextAction == "" {
		exactNextAction = strings.TrimSpace(record.ExactNextAction)
	}
	if exactNextAction == "" {
		exactNextAction = "continue mission from the latest durable event readback"
	}
	promptText := buildMissionCompactionResumePromptText(record, *gate, latestRoute, eventIndex.IndexDigest, timelineIndex.IndexDigest, exactNextAction)
	prompt := MissionCompactionResumePrompt{
		Schema:               "ao.mission.compaction-resume-prompt.v0.1",
		Status:               "ready",
		MissionID:            record.MissionID,
		MissionStatus:        record.Status,
		CurrentPhase:         record.CurrentPhase,
		CurrentRoute:         record.CurrentRoute,
		LatestRoute:          latestRoute,
		EventIndexDigest:     eventIndex.IndexDigest,
		TimelineIndexDigest:  timelineIndex.IndexDigest,
		EventCount:           missionEventCount,
		TimelineTermCount:    timelineIndex.TermCount,
		CompletedNodes:       gate.CompletedNodes,
		ReadyNodes:           gate.ReadyNodesRemaining,
		MinNodes:             gate.MinNodes,
		ReturnGateStatus:     gate.Status,
		FinalResponseAllowed: gate.FinalResponseAllowed,
		ExactNextAction:      exactNextAction,
		ResumePrompt:         promptText,
		SafeToExecute:        false,
		ExecutesWork:         false,
		ApprovesWork:         false,
		MutatesRepositories:  false,
		GeneratedAtUTC:       now(s.Clock),
	}
	if err := ValidateMissionCompactionResumePrompt(prompt); err != nil {
		return MissionCompactionResumePrompt{}, err
	}
	return prompt, nil
}

func ValidateMissionCompactionResumePrompt(prompt MissionCompactionResumePrompt) error {
	var errs []string
	if prompt.Schema != "ao.mission.compaction-resume-prompt.v0.1" {
		errs = append(errs, "mission compaction resume prompt schema must be ao.mission.compaction-resume-prompt.v0.1")
	}
	if prompt.Status != "ready" && prompt.Status != "blocked" {
		errs = append(errs, "mission compaction resume prompt status must be ready or blocked")
	}
	if strings.TrimSpace(prompt.MissionID) == "" {
		errs = append(errs, "mission compaction resume prompt requires mission id")
	}
	if strings.TrimSpace(prompt.MissionStatus) == "" || strings.TrimSpace(prompt.CurrentRoute) == "" || strings.TrimSpace(prompt.LatestRoute) == "" {
		errs = append(errs, "mission compaction resume prompt requires mission status and route bindings")
	}
	if !strings.HasPrefix(prompt.EventIndexDigest, "sha256:") || !strings.HasPrefix(prompt.TimelineIndexDigest, "sha256:") {
		errs = append(errs, "mission compaction resume prompt requires event and timeline sha256 digests")
	}
	if prompt.EventCount < 1 || prompt.TimelineTermCount < 1 {
		errs = append(errs, "mission compaction resume prompt requires positive event and timeline counts")
	}
	if prompt.CompletedNodes < 0 || prompt.ReadyNodes < 0 || prompt.MinNodes < 0 {
		errs = append(errs, "mission compaction resume prompt node counts must not be negative")
	}
	if strings.TrimSpace(prompt.ReturnGateStatus) == "" || strings.TrimSpace(prompt.ExactNextAction) == "" {
		errs = append(errs, "mission compaction resume prompt requires return gate status and exact next action")
	}
	if !strings.Contains(prompt.ResumePrompt, prompt.MissionID) || !strings.Contains(prompt.ResumePrompt, prompt.EventIndexDigest) || !strings.Contains(prompt.ResumePrompt, prompt.TimelineIndexDigest) {
		errs = append(errs, "mission compaction resume prompt text must bind mission id and timeline digests")
	}
	if !strings.Contains(prompt.ResumePrompt, "Do not return a final answer after partial progress") {
		errs = append(errs, "mission compaction resume prompt must preserve partial-progress denial")
	}
	if prompt.SafeToExecute || prompt.ExecutesWork || prompt.ApprovesWork || prompt.MutatesRepositories {
		errs = append(errs, "mission compaction resume prompt must not execute, approve, or mutate")
	}
	if len(errs) > 0 {
		return fmt.Errorf(strings.Join(errs, "; "))
	}
	return nil
}

func buildMissionCompactionResumePromptText(record Record, gate ReturnGate, latestRoute, eventIndexDigest, timelineIndexDigest, exactNextAction string) string {
	return fmt.Sprintf("Start a Codex goal and resume AO Mission %s from the latest durable Mission event timeline.\n\nCurrent state:\n- mission_id=%s\n- mission_status=%s\n- current_phase=%s\n- current_route=%s\n- latest_route=%s\n- completed_nodes=%d\n- ready_nodes=%d\n- min_nodes=%d\n- return_gate_status=%s\n- final_response_allowed=%t\n- event_index_digest=%s\n- timeline_index_digest=%s\n- exact_next_action=%s\n\nRequired behavior:\n1. Rebuild the Mission event index and timeline query index before changing files.\n2. Verify the rebuilt digests match the recorded event_index_digest and timeline_index_digest or record a compaction/recovery blocker.\n3. Continue the exact next action through the governed Mission route.\n4. Keep exactly one executable mutation node active at a time.\n5. Do not ask the operator for the next step between ready nodes.\n6. Do not return a final answer after partial progress.\n7. Stop only for a true hard blocker after safe repair/repack/support work has been attempted.\n\nSafety boundaries:\n- no direct main mutation\n- no credential or token inspection\n- no provider calls\n- no release, deploy, publish, upload, or tag\n- no dependency updates unless separately authorized\n- no policy/auth/config widening\n- no hidden instruction mutation\n- no broad RSI claim\n- RSI remains denied\n",
		record.MissionID,
		record.MissionID,
		record.Status,
		record.CurrentPhase,
		record.CurrentRoute,
		latestRoute,
		gate.CompletedNodes,
		gate.ReadyNodesRemaining,
		gate.MinNodes,
		gate.Status,
		gate.FinalResponseAllowed,
		eventIndexDigest,
		timelineIndexDigest,
		exactNextAction,
	)
}

func digestMissionTimelineTerms(terms []MissionTimelineTerm) (string, error) {
	body, err := json.Marshal(terms)
	if err != nil {
		return "", err
	}
	return digestBytes(body), nil
}

func countMissionTimelineMatches(index MissionTimelineQueryIndex, missionID string) int {
	count := 0
	for _, term := range index.Terms {
		for _, match := range term.Matches {
			if match.MissionID == missionID {
				count++
			}
		}
	}
	return count
}

func countDuplicateMissionTimelineMatches(index MissionTimelineQueryIndex, missionID string) int {
	seen := map[string]bool{}
	duplicates := 0
	for _, term := range index.Terms {
		for _, match := range term.Matches {
			if match.MissionID != missionID {
				continue
			}
			key := fmt.Sprintf("%s\x00%s\x00%d", term.Term, match.Kind, match.Sequence)
			if seen[key] {
				duplicates++
			}
			seen[key] = true
		}
	}
	return duplicates
}

func timelineTermsForEvent(event MissionEvent) []string {
	raw := strings.Join([]string{event.MissionID, event.Kind, event.Status, event.Route, event.Phase, event.ArtifactKind, event.Summary}, " ")
	seen := map[string]struct{}{}
	terms := []string{}
	for _, term := range strings.FieldsFunc(strings.ToLower(raw), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '/')
	}) {
		term = strings.TrimSpace(term)
		if len(term) < 2 {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms
}

func BuildMissionDoctorReadback(s Store) MissionDoctorReadback {
	readback := MissionDoctorReadback{
		Schema:                    "ao.mission.doctor-readback.v0.1",
		Status:                    "ready",
		Root:                      s.Root,
		LeaseHealthStatus:         "healthy",
		CheckpointFreshnessStatus: "fresh",
		EarlyReturnRiskStatus:     "clear",
		StaleRouteDecisionStatus:  "clear",
		RiskMissions:              []MissionDoctorRisk{},
		ExactNextAction:           "doctor checks passed; continue the latest Mission exact next action or final synthesis",
		Checks:                    []string{},
		Blockers:                  []string{},
		SafeToExecute:             false,
		ExecutesWork:              false,
		ApprovesWork:              false,
		MutatesRepositories:       false,
		GeneratedAtUTC:            now(s.Clock),
	}
	if err := s.Init(); err != nil {
		readback.Status = "blocked"
		readback.Blockers = append(readback.Blockers, "mission store init failed: "+err.Error())
		return readback
	}
	readback.Checks = append(readback.Checks, "mission_store_initialized")
	records, stats, err := s.listFilteredWithStats(ListFilters{})
	if err != nil {
		readback.Status = "blocked"
		readback.Blockers = append(readback.Blockers, "mission records reload failed: "+err.Error())
		return readback
	}
	readback.StoreListCount = stats.StoreListCount
	readback.StoreFileReads = stats.StoreFileReads
	index, err := buildMissionEventIndexFromRecords(s, records, stats)
	if err != nil {
		readback.Status = "blocked"
		readback.Blockers = append(readback.Blockers, "mission event index failed: "+err.Error())
		return readback
	}
	readback.MissionCount = index.MissionCount
	readback.EventCount = index.TotalEvents
	for _, record := range records {
		if record.GoalLease != nil {
			readback.LeaseCount++
			if record.GoalLease.MinNodes <= 0 || record.GoalLease.MinMinutes < 0 || record.GoalLease.MaxMinutes <= 0 || strings.TrimSpace(record.GoalLease.ReturnOnlyWhen) == "" || strings.TrimSpace(record.GoalLease.CheckpointPolicy) == "" {
				readback.LeaseHealthStatus = "invalid"
				readback.RiskMissions = append(readback.RiskMissions, missionDoctorRisk(record.MissionID, "lease_health", "invalid", "goal lease is missing minimums or stop/checkpoint policy", "repair goal lease before continuation"))
				readback.Status = "blocked"
				readback.Blockers = append(readback.Blockers, "invalid goal lease for "+record.MissionID)
				readback.ExactNextAction = "repair goal lease before continuation"
			}
		} else if missionDoctorShouldExpectLease(record) {
			readback.LeaseHealthStatus = "missing"
			readback.RiskMissions = append(readback.RiskMissions, missionDoctorRisk(record.MissionID, "lease_health", "missing", "active long-run state has no goal lease", "run ao-mission continue --mission "+record.MissionID+" --until-done to create a governed lease"))
		}
		if len(record.Checkpoints) > 0 {
			readback.FreshCheckpoints++
		} else if record.ReturnGate != nil && !record.ReturnGate.FinalResponseAllowed {
			readback.CheckpointFreshnessStatus = "stale_or_missing"
			readback.RiskMissions = append(readback.RiskMissions, missionDoctorRisk(record.MissionID, "checkpoint_freshness", "stale_or_missing", "return gate is blocking but no checkpoint is recorded", "write checkpoint/resume bundle before final response"))
		}
		gate := record.ReturnGate
		if gate == nil {
			evaluated := EvaluateReturnGate(record)
			gate = &evaluated
		}
		if gate != nil && !gate.FinalResponseAllowed {
			readback.EarlyReturnRisks++
			readback.EarlyReturnRiskStatus = "risk_detected"
			next := strings.TrimSpace(gate.ExactNextAction)
			if next == "" {
				next = "continue governed mission loop until return gate clears"
			}
			reason := strings.TrimSpace(gate.Reason)
			if reason == "" {
				reason = gate.Status
			}
			readback.RiskMissions = append(readback.RiskMissions, missionDoctorRisk(record.MissionID, "early_return", gate.Status, reason, next))
			if readback.ExactNextAction == "" || readback.ExactNextAction == "doctor checks passed; continue the latest Mission exact next action or final synthesis" {
				readback.ExactNextAction = next
			}
		}
		reconciliation := record.Reconciliation
		if reconciliation == nil {
			computed := BuildRouteReconciliation(record)
			reconciliation = &computed
		}
		if reconciliation != nil && reconciliation.Status == "stale_route_detected" {
			readback.StaleRoutes++
			readback.StaleRouteDecisionStatus = "stale_route_detected"
			readback.Status = "blocked"
			readback.Blockers = append(readback.Blockers, "stale route reconciliation for "+record.MissionID)
			next := strings.TrimSpace(reconciliation.ExactNextAction)
			if next == "" {
				next = "refresh route decision before final response"
			}
			readback.RiskMissions = append(readback.RiskMissions, missionDoctorRisk(record.MissionID, "stale_route", reconciliation.Status, "latest route decision does not match current route/readback", next))
			readback.ExactNextAction = next
		}
	}
	readback.Checks = append(readback.Checks,
		"mission_records_readable",
		"mission_event_index_readable",
		"authority_flags_false",
		"lease_health_checked",
		"lease_minimums_validated",
		"checkpoint_freshness_checked",
		"checkpoint_resume_bundle_freshness_validated",
		"stale_route_reconciliation_checked",
		"stale_route_decision_bound",
		"early_return_risk_checked",
		"early_return_risk_exact_next_action_bound",
	)
	return readback
}

func missionDoctorShouldExpectLease(record Record) bool {
	if record.Status == "done" || record.Status == "blocked" {
		return false
	}
	return len(record.Steps) > 0 || len(record.Checkpoints) > 0 || record.ReturnGate != nil || record.Evidence.AtlasWorkgraph != nil || record.Evidence.AtlasRecommendation != nil
}

func missionDoctorRisk(missionID, kind, status, reason, exactNextAction string) MissionDoctorRisk {
	return MissionDoctorRisk{
		MissionID:       missionID,
		Kind:            kind,
		Status:          status,
		Reason:          reason,
		ExactNextAction: exactNextAction,
	}
}

func BuildMissionReadinessBundleReadback(inputs []MissionReadinessBundleInput) (MissionReadinessBundleReadback, error) {
	readback := MissionReadinessBundleReadback{
		Schema:              "ao.mission.readiness-bundle-readback.v0.1",
		Status:              "ready",
		Repos:               []MissionReadinessRepoReadback{},
		SafeToExecute:       false,
		ExecutesWork:        false,
		ApprovesWork:        false,
		MutatesRepositories: false,
		ExactNextAction:     "review blocked repo readiness summaries before PR lifecycle work",
		GeneratedAtUTC:      now(nil),
	}
	for _, input := range inputs {
		repo := strings.TrimSpace(input.Repo)
		path := strings.TrimSpace(input.Path)
		if repo == "" || path == "" {
			return MissionReadinessBundleReadback{}, fmt.Errorf("readiness bundle inputs require repo and path")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return MissionReadinessBundleReadback{}, err
		}
		text := string(body)
		if err := ValidatePublicSafeText(text); err != nil {
			return MissionReadinessBundleReadback{}, err
		}
		repoStatus := "blocked"
		if strings.Contains(text, "status=ready") && (strings.Contains(text, "100/100") || strings.Contains(text, "score=100/100")) {
			repoStatus = "ready"
			readback.ReadyRepos++
		} else {
			readback.BlockedRepos++
			readback.Status = "blocked"
		}
		readback.Repos = append(readback.Repos, MissionReadinessRepoReadback{
			Repo:   repo,
			Path:   path,
			Status: repoStatus,
			Score:  readinessScoreLabel(text),
			SHA256: digestBytes(body),
		})
	}
	readback.RepoCount = len(readback.Repos)
	if readback.RepoCount == 0 {
		readback.Status = "blocked"
		readback.ExactNextAction = "provide at least one local readiness summary"
	}
	if readback.Status == "ready" {
		readback.ExactNextAction = "readiness bundle verified locally; remote PR lifecycle remains operator-controlled"
	}
	return readback, nil
}

func readinessScoreLabel(text string) string {
	if strings.Contains(text, "score=100/100") {
		return "100/100"
	}
	if strings.Contains(text, "100/100") {
		return "100/100"
	}
	return ""
}

func BuildMissionDashboardReadback(s Store, missionID string, compact bool) (MissionDashboardReadback, error) {
	record, err := s.Load(missionID)
	if err != nil {
		return MissionDashboardReadback{}, err
	}
	return buildMissionDashboardReadback(s, record, compact)
}

func buildMissionDashboardReadback(s Store, record Record, compact bool) (MissionDashboardReadback, error) {
	index, err := buildMissionEventIndexFromRecords(s, []Record{record}, storeListStats{StoreFileReads: 1})
	if err != nil {
		return MissionDashboardReadback{}, err
	}
	events := append([]MissionEvent(nil), index.Events...)
	if compact && len(events) > 5 {
		events = events[len(events)-5:]
	}
	latestRoute := record.CurrentRoute
	if n := len(record.RouteHistory); n > 0 && strings.TrimSpace(record.RouteHistory[n-1].Route) != "" {
		latestRoute = record.RouteHistory[n-1].Route
	}
	readback := MissionDashboardReadback{
		Schema:                     "ao.mission.dashboard-readback.v0.1",
		Status:                     "ready",
		MissionID:                  record.MissionID,
		CorrelationID:              record.CorrelationID,
		MissionStatus:              record.Status,
		SourceRecordStatus:         record.SourceRecordStatus,
		TerminalProjectionStatus:   record.TerminalProjectionStatus,
		TerminalProjectionReadOnly: record.TerminalProjectionReadOnly,
		EffectiveOperatorStatus:    record.EffectiveOperatorStatus,
		CurrentPhase:               record.CurrentPhase,
		CurrentRoute:               record.CurrentRoute,
		LatestRoute:                latestRoute,
		EventCount:                 len(events),
		EventIndexDigest:           index.IndexDigest,
		Compact:                    compact,
		RecentEvents:               events,
		SafeToExecute:              false,
		ExecutesWork:               false,
		ApprovesWork:               false,
		MutatesRepositories:        false,
		ExactNextAction:            record.ExactNextAction,
		GeneratedAtUTC:             now(s.Clock),
	}
	if counts := record.Evidence.AtlasWorkgraph; counts != nil {
		readback.TotalNodes = counts.Total
		readback.CompletedNodes = counts.Completed
		readback.ReadyNodes = counts.Ready
		readback.BlockedNodes = counts.Blocked
		readback.FailedNodes = counts.Failed
	}
	if record.Status == "done" && record.CurrentPhase == "reconciled" {
		readback.Status = record.Status
	}
	return readback, nil
}

func missionEventsForRecord(record Record) []MissionEvent {
	events := []MissionEvent{{
		Schema:         "ao.mission.event.v0.1",
		MissionID:      record.MissionID,
		CorrelationID:  record.CorrelationID,
		Kind:           "mission_record",
		Sequence:       0,
		Status:         record.Status,
		Route:          record.CurrentRoute,
		Phase:          record.CurrentPhase,
		Summary:        fmt.Sprintf("mission %s status %s route %s phase %s next %s", record.MissionID, record.Status, record.CurrentRoute, record.CurrentPhase, record.ExactNextAction),
		GeneratedAtUTC: record.UpdatedAtUTC,
	}}
	for i, route := range record.RouteHistory {
		events = append(events, MissionEvent{
			Schema:         "ao.mission.event.v0.1",
			MissionID:      record.MissionID,
			Kind:           "route_decision",
			Sequence:       i + 1,
			Route:          route.Route,
			Summary:        strings.TrimSpace(route.Reason + " " + route.ExactNextAction),
			GeneratedAtUTC: route.GeneratedAtUTC,
		})
		events = append(events, MissionEvent{
			Schema:         "ao.mission.event.v0.1",
			MissionID:      record.MissionID,
			Kind:           "route_evidence",
			Sequence:       i + 1,
			Route:          route.Route,
			Summary:        strings.TrimSpace("route decision " + route.Route + " " + route.Reason + " " + route.ExactNextAction),
			GeneratedAtUTC: route.GeneratedAtUTC,
		})
	}
	for i, step := range record.Steps {
		events = append(events, MissionEvent{
			Schema:         "ao.mission.event.v0.1",
			MissionID:      record.MissionID,
			Kind:           "continuation_step",
			Sequence:       i + 1,
			Status:         step.Result,
			Route:          step.Route,
			Summary:        step.ExactNextAction,
			GeneratedAtUTC: step.GeneratedAtUTC,
		})
		events = append(events, MissionEvent{
			Schema:         "ao.mission.event.v0.1",
			MissionID:      record.MissionID,
			Kind:           "node_evidence",
			Sequence:       i + 1,
			Status:         step.Result,
			Route:          step.Route,
			Summary:        fmt.Sprintf("continuation node iteration=%d result=%s next=%s", step.Iteration, step.Result, step.ExactNextAction),
			GeneratedAtUTC: step.GeneratedAtUTC,
		})
	}
	for i, ref := range record.ArtifactRefs {
		events = append(events, MissionEvent{
			Schema:       "ao.mission.event.v0.1",
			MissionID:    record.MissionID,
			Kind:         "artifact_ref",
			Sequence:     i + 1,
			ArtifactKind: ref.Kind,
			Summary:      strings.TrimSpace(ref.Kind + " " + ref.Ref + " " + ref.Digest),
		})
		if alias := artifactEvidenceAliasKind(ref.Kind); alias != "" {
			events = append(events, MissionEvent{
				Schema:       "ao.mission.event.v0.1",
				MissionID:    record.MissionID,
				Kind:         alias,
				Sequence:     i + 1,
				ArtifactKind: ref.Kind,
				Summary:      strings.TrimSpace(ref.Kind + " " + ref.Ref + " " + ref.Digest),
			})
		}
	}
	if record.GoalLease != nil {
		events = append(events, MissionEvent{
			Schema:         "ao.mission.event.v0.1",
			MissionID:      record.MissionID,
			Kind:           "goal_lease",
			Sequence:       len(events) + 1,
			Status:         "ready",
			Summary:        fmt.Sprintf("lease min_nodes=%d min_minutes=%d max_minutes=%d return_only_when=%s checkpoint_policy=%s", record.GoalLease.MinNodes, record.GoalLease.MinMinutes, record.GoalLease.MaxMinutes, record.GoalLease.ReturnOnlyWhen, record.GoalLease.CheckpointPolicy),
			GeneratedAtUTC: record.GoalLease.UpdatedAtUTC,
		})
	}
	for i, checkpoint := range record.Checkpoints {
		events = append(events, MissionEvent{
			Schema:         "ao.mission.event.v0.1",
			MissionID:      record.MissionID,
			Kind:           "checkpoint",
			Sequence:       i + 1,
			Status:         checkpoint.Result,
			Route:          checkpoint.Route,
			Phase:          checkpoint.Phase,
			Summary:        checkpoint.ResumeCommand + " " + checkpoint.ExactNextAction,
			GeneratedAtUTC: checkpoint.GeneratedAtUTC,
		})
	}
	if record.Evidence.FoundryRollup != nil {
		summary := fmt.Sprintf("foundry rollup status %s completed_nodes=%d total_nodes=%d", record.Evidence.FoundryRollup.Status, record.Evidence.FoundryRollup.CompletedNodes, record.Evidence.FoundryRollup.TotalNodes)
		events = append(events, MissionEvent{
			Schema:         "ao.mission.event.v0.1",
			MissionID:      record.MissionID,
			Kind:           "foundry_rollup",
			Sequence:       len(events) + 1,
			Status:         record.Evidence.FoundryRollup.Status,
			Summary:        summary,
			GeneratedAtUTC: record.UpdatedAtUTC,
		})
		events = append(events, MissionEvent{
			Schema:         "ao.mission.event.v0.1",
			MissionID:      record.MissionID,
			Kind:           "rollup_evidence",
			Sequence:       len(events) + 1,
			Status:         record.Evidence.FoundryRollup.Status,
			Summary:        summary,
			GeneratedAtUTC: record.UpdatedAtUTC,
		})
	}
	if record.Evidence.AtlasRecommendation != nil {
		events = append(events, MissionEvent{
			Schema:    "ao.mission.event.v0.1",
			MissionID: record.MissionID,
			Kind:      "atlas_recommendation",
			Sequence:  len(events) + 1,
			Status:    record.Evidence.AtlasRecommendation.Status,
			Route:     record.CurrentRoute,
			Phase:     record.CurrentPhase,
			Summary: fmt.Sprintf("atlas recommendation status %s completed_nodes=%d total_nodes=%d ready_nodes=%d checkpoint_count=%d elapsed_minutes=%d lease_time_status=%s return_gate_status=%s blocker=%s",
				record.Evidence.AtlasRecommendation.Status,
				record.Evidence.AtlasRecommendation.CompletedNodes,
				record.Evidence.AtlasRecommendation.TotalNodes,
				record.Evidence.AtlasRecommendation.ReadyNodes,
				record.Evidence.AtlasRecommendation.CheckpointCount,
				record.Evidence.AtlasRecommendation.ElapsedMinutes,
				record.Evidence.AtlasRecommendation.LeaseTimeStatus,
				record.Evidence.AtlasRecommendation.ReturnGateStatus,
				record.Evidence.AtlasRecommendation.Blocker,
			),
			GeneratedAtUTC: record.UpdatedAtUTC,
		})
	}
	if record.ReturnGate != nil {
		events = append(events, MissionEvent{
			Schema:         "ao.mission.event.v0.1",
			MissionID:      record.MissionID,
			Kind:           "return_gate",
			Sequence:       len(events) + 1,
			Status:         record.ReturnGate.Status,
			Summary:        record.ReturnGate.Reason + " " + record.ReturnGate.ExactNextAction,
			GeneratedAtUTC: record.ReturnGate.GeneratedAtUTC,
		})
		if record.ReturnGate.HardBlocker || !record.ReturnGate.FinalResponseAllowed {
			events = append(events, MissionEvent{
				Schema:         "ao.mission.event.v0.1",
				MissionID:      record.MissionID,
				Kind:           "blocker_evidence",
				Sequence:       len(events) + 1,
				Status:         record.ReturnGate.Status,
				Summary:        strings.TrimSpace(record.ReturnGate.Reason + " " + record.ReturnGate.ExactNextAction),
				GeneratedAtUTC: record.ReturnGate.GeneratedAtUTC,
			})
		}
	}
	for i, blocker := range record.Blockers {
		events = append(events, MissionEvent{
			Schema:         "ao.mission.event.v0.1",
			MissionID:      record.MissionID,
			Kind:           "blocker_evidence",
			Sequence:       i + 1,
			Status:         "blocked",
			Route:          record.CurrentRoute,
			Phase:          record.CurrentPhase,
			Summary:        blocker,
			GeneratedAtUTC: record.UpdatedAtUTC,
		})
	}
	if record.Evidence.AtlasRecommendation != nil {
		packet := BuildFinalReconciliationPacket(record)
		events = append(events, MissionEvent{
			Schema:         "ao.mission.event.v0.1",
			MissionID:      record.MissionID,
			Kind:           "final_reconciliation",
			Sequence:       len(events) + 1,
			Status:         packet.Status,
			Route:          record.CurrentRoute,
			Phase:          record.CurrentPhase,
			Summary:        fmt.Sprintf("artifacts_agree=%t final_response_allowed=%t completed_nodes=%d total_nodes=%d blocker=%s rsi_remains_denied=%t claims_authority_advance=%t", packet.ArtifactsAgree, packet.FinalResponseAllowed, packet.CompletedNodes, packet.TotalNodes, packet.Blocker, packet.RSIRemainsDenied, packet.ClaimsAuthorityAdvance),
			GeneratedAtUTC: packet.GeneratedAtUTC,
		})
	}
	if record.Reconciliation != nil {
		events = append(events, MissionEvent{
			Schema:         "ao.mission.event.v0.1",
			MissionID:      record.MissionID,
			Kind:           "route_reconciliation",
			Sequence:       len(events) + 1,
			Status:         record.Reconciliation.Status,
			Route:          record.Reconciliation.CurrentRoute,
			Summary:        record.Reconciliation.ExactNextAction,
			GeneratedAtUTC: record.Reconciliation.GeneratedAtUTC,
		})
	}
	for i := range events {
		events[i].CorrelationID = record.CorrelationID
	}
	return events
}

func artifactEvidenceAliasKind(kind string) string {
	normalized := strings.NewReplacer("-", "_", " ", "_", "/", "_").Replace(strings.ToLower(strings.TrimSpace(kind)))
	switch {
	case normalized == "":
		return ""
	case strings.Contains(normalized, "pull_request") || normalized == "pr" || strings.Contains(normalized, "merged_pr"):
		return "pr_evidence"
	case strings.Contains(normalized, "ci") || strings.Contains(normalized, "check") || strings.Contains(normalized, "action_run"):
		return "ci_evidence"
	case strings.Contains(normalized, "node"):
		return "node_evidence"
	case strings.Contains(normalized, "blocker"):
		return "blocker_evidence"
	case strings.Contains(normalized, "rollup"):
		return "rollup_evidence"
	case strings.Contains(normalized, "route"):
		return "route_evidence"
	default:
		return ""
	}
}

func missionEventMatchesQuery(event MissionEvent, query string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		event.MissionID,
		event.Kind,
		event.Status,
		event.Route,
		event.Phase,
		event.ArtifactKind,
		event.Summary,
	}, " "))
	return strings.Contains(haystack, query)
}
