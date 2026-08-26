package mission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const correlationEvidenceImportKind = "correlation-evidence"

var atlasWorkgraphNodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

type ImportReadback struct {
	Schema                 string      `json:"schema"`
	MissionID              string      `json:"mission_id"`
	CorrelationID          string      `json:"correlation_id,omitempty"`
	CorrelationChainDigest string      `json:"correlation_chain_digest,omitempty"`
	Kind                   string      `json:"kind"`
	Status                 string      `json:"status"`
	Artifact               ArtifactRef `json:"artifact"`
	ExactNextAction        string      `json:"exact_next_action"`
	SafeToExecute          bool        `json:"safe_to_execute"`
	ExecutesWork           bool        `json:"executes_work"`
	ApprovesWork           bool        `json:"approves_work"`
	GeneratedAtUTC         string      `json:"generated_at_utc"`
}

func ImportArtifact(s Store, missionID, kind, path string) (ImportReadback, error) {
	return importArtifact(s, missionID, kind, path, "", kind, false)
}

func ImportArtifactWithCorrelationChain(s Store, missionID, kind, path, chainPath string) (ImportReadback, error) {
	chainPath = strings.TrimSpace(chainPath)
	if chainPath == "" {
		return ImportReadback{}, fmt.Errorf("correlated import requires a correlation chain")
	}
	return importArtifact(s, missionID, kind, path, chainPath, kind, false)
}

func ImportCorrelationEvidence(
	s Store,
	missionID, path, chainPath, correlationRole string,
) (ImportReadback, error) {
	chainPath = strings.TrimSpace(chainPath)
	if chainPath == "" {
		return ImportReadback{}, fmt.Errorf("correlation-evidence import requires a correlation chain")
	}
	correlationRole = strings.TrimSpace(correlationRole)
	if !correlationRolePattern.MatchString(correlationRole) {
		return ImportReadback{}, fmt.Errorf("correlation-evidence import requires a valid correlation role")
	}
	return importArtifact(
		s,
		missionID,
		correlationEvidenceImportKind,
		path,
		chainPath,
		correlationRole,
		true,
	)
}

func importArtifact(
	s Store,
	missionID, kind, path, chainPath, correlationRole string,
	neutral bool,
) (ImportReadback, error) {
	if kind == aoNextJournalPrefixImportKind && chainPath != "" {
		return ImportReadback{}, fmt.Errorf("AO Next journal prefix import does not accept a correlation chain")
	}
	var existing Record
	var body []byte
	var err error
	refPath := path
	var chainReference *CorrelationChainReference
	var correlatedBinding *CorrelatedImportBinding
	chainDigest := ""
	if chainPath == "" {
		switch kind {
		case aoNextCandidateImportKind:
			body, err = readBoundedRegularFile(path, aoNextCandidateInputLimit)
		case aoNextJournalPrefixImportKind:
			refPath, body, err = readBoundedAbsoluteRegularFileOutsideRoot(path, s.Root, aoNextJournalPrefixInputLimit)
		default:
			body, err = os.ReadFile(path)
		}
	} else {
		existing, err = s.Load(missionID)
		if err == nil {
			var chain CorrelationChain
			var validation CorrelationChainValidation
			chain, validation, err = loadValidatedCorrelationChainForRecord(chainPath, existing)
			if err == nil {
				refPath, body, err = readCanonicalCorrelationArtifact(path)
			}
			if err == nil {
				entry, present := correlationChainEntry(chain, correlationRole)
				actualDigest := digestBytes(body)
				if !present || entry.Digest != actualDigest {
					err = fmt.Errorf("imported artifact role %q and digest %s are absent from correlation chain", correlationRole, actualDigest)
				} else {
					chainAbsolutePath, pathErr := filepath.Abs(chainPath)
					var chainCanonicalPath string
					if pathErr == nil {
						chainCanonicalPath, pathErr = filepath.EvalSymlinks(chainAbsolutePath)
					}
					if pathErr != nil {
						err = pathErr
					} else {
						reference := correlationChainReference(
							chain,
							validation.ChainDigest,
							filepath.Dir(chainCanonicalPath),
						)
						binding := CorrelatedImportBinding{
							Role:            correlationRole,
							Digest:          actualDigest,
							ArtifactPath:    refPath,
							LocatorState:    correlationLocatorStateLive,
							ChainDigest:     validation.ChainDigest,
							ReferenceDigest: reference.ReferenceDigest,
						}
						chainReference = &reference
						correlatedBinding = &binding
						chainDigest = validation.ChainDigest
					}
				}
			}
		}
	}
	if err != nil {
		return ImportReadback{}, err
	}
	if kind != aoNextCandidateImportKind && kind != aoNextJournalPrefixImportKind {
		if err := ValidatePublicSafeText(string(body)); err != nil {
			return ImportReadback{}, err
		}
	}
	if err := validateNoDuplicateJSONKeys(body); err != nil {
		return ImportReadback{}, err
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return ImportReadback{}, err
	}
	var aoNextProjection *AONextCandidateProjection
	if kind == aoNextCandidateImportKind {
		projection, parseErr := parseAONextCandidateProjection(body)
		if parseErr != nil {
			return ImportReadback{}, parseErr
		}
		aoNextProjection = &projection
	}
	var aoNextJournalProjection *AONextJournalProjection
	var aoNextJournalRunID string
	if kind == aoNextJournalPrefixImportKind {
		prefix, parseErr := parseAONextJournalPrefix(body)
		if parseErr != nil {
			return ImportReadback{}, parseErr
		}
		status, projectErr := projectAONextJournalPrefix(prefix)
		if projectErr != nil {
			return ImportReadback{}, projectErr
		}
		aoNextJournalRunID = prefix.RunID
		aoNextJournalProjection = &AONextJournalProjection{
			Schema:       "ao.mission.ao-next-journal-projection.v1",
			RunID:        prefix.RunID,
			PrefixDigest: prefix.PrefixDigest,
			Status:       status,
			ReadOnly:     true,
		}
	}
	if isMissionEvidenceReadback(kind) {
		for _, field := range []string{"safe_to_execute", "schedules_work", "executes_work", "approves_work", "mutates_repositories", "provider_calls", "release_or_publish", "credential_use", "direct_main_mutation", "concurrent_mutation", "claims_authority_advance"} {
			if boolFromAny(doc[field]) {
				return ImportReadback{}, fmt.Errorf("%s %s must be false", kind, field)
			}
		}
	}
	if existing.MissionID == "" {
		existing, err = s.Load(missionID)
		if err != nil {
			return ImportReadback{}, err
		}
	}
	if kind == aoNextJournalPrefixImportKind && existing.Evidence.AONextJournalProjection != nil {
		current := existing.Evidence.AONextJournalProjection
		candidateDigest := digestBytes(body)
		if current.RunID == aoNextJournalRunID && current.ArtifactDigest != candidateDigest {
			return ImportReadback{}, fmt.Errorf("AO Next journal run already bound to a different digest")
		}
		if current.ArtifactDigest == candidateDigest {
			for _, existingRef := range existing.ArtifactRefs {
				if existingRef.Kind == kind && existingRef.Digest == candidateDigest {
					return ImportReadback{
						Schema:          "ao.mission.import-readback.v0.1",
						MissionID:       existing.MissionID,
						CorrelationID:   existing.CorrelationID,
						Kind:            kind,
						Status:          "recorded",
						Artifact:        existingRef,
						ExactNextAction: existing.ExactNextAction,
						GeneratedAtUTC:  now(nil),
					}, nil
				}
			}
			return ImportReadback{}, fmt.Errorf("AO Next journal projection is missing its retained artifact reference")
		}
	}
	if existing.CorrelationID != "" && chainReference == nil && kind != aoNextJournalPrefixImportKind {
		correlationID := stringFromAny(doc["correlation_id"])
		if correlationID == "" {
			return ImportReadback{}, fmt.Errorf("%s correlation_id is required for correlated mission", kind)
		}
		if correlationID != existing.CorrelationID {
			return ImportReadback{}, fmt.Errorf("%s correlation_id does not match mission", kind)
		}
	}
	if err := validateCorrelatedAtlasWorkgraphIdentity(existing, kind, doc); err != nil {
		return ImportReadback{}, err
	}
	atlasWorkgraphNextAction := ""
	if kind == "atlas-workgraph" {
		atlasWorkgraphNextAction, err = firstReadyAtlasWorkgraphNode(doc)
		if err != nil {
			return ImportReadback{}, err
		}
	}
	contentRef, digest, err := s.retainArtifact(body)
	if err != nil {
		return ImportReadback{}, err
	}
	ref := ArtifactRef{Schema: ArtifactRefSchema, Ref: refPath, ContentRef: contentRef, Digest: digest, Kind: kind}
	r, err := s.updateWithCheckpointTransaction(missionID, func(rec *Record) error {
		if chainReference != nil &&
			(rec.MissionID != chainReference.MissionID || rec.CorrelationID != chainReference.CorrelationID) {
			return fmt.Errorf("correlation chain identity changed before import")
		}
		if kind == aoNextCandidateImportKind && rec.Evidence.AONextCandidate != nil {
			if rec.Evidence.AONextCandidate.ArtifactDigest == ref.Digest {
				return nil
			}
			if rec.Evidence.AONextCandidate.RunID == aoNextProjection.RunID {
				return fmt.Errorf("AO Next candidate run already bound to a different digest")
			}
		}
		if kind == aoNextJournalPrefixImportKind && rec.Evidence.AONextJournalProjection != nil {
			if rec.Evidence.AONextJournalProjection.ArtifactDigest == ref.Digest {
				return nil
			}
			if rec.Evidence.AONextJournalProjection.RunID == aoNextJournalRunID {
				return fmt.Errorf("AO Next journal run already bound to a different digest")
			}
		}
		for _, existingRef := range rec.ArtifactRefs {
			if existingRef.Ref != ref.Ref || existingRef.Kind != ref.Kind {
				continue
			}
			if existingRef.Digest != ref.Digest {
				return fmt.Errorf("artifact path already bound to a different digest")
			}
			if correlatedBinding == nil {
				return nil
			}
		}
		rec.ArtifactRefs = append(rec.ArtifactRefs, ref)
		switch {
		case neutral:
			// Neutral evidence records provenance without changing Mission workflow state.
		case kind == "blueprint-authorization":
			rec.CurrentRoute = "ao-atlas"
			rec.CurrentPhase = "blueprint_authorized"
			rec.ExactNextAction = "send authorized Blueprint pack to AO Atlas"
			AppendRouteHistory(rec, routeFromRecord(*rec, "Blueprint authorization imported"))
		case kind == "atlas-workgraph":
			counts := countWorkgraphNodes(doc)
			rec.Evidence.AtlasWorkgraph = &counts
			if counts.Ready > 0 {
				rec.Status = "active"
				rec.Blockers = []string{}
				rec.Evidence.AtlasRecommendation = nil
				rec.Evidence.AtlasFinalSynthesis = nil
				rec.Evidence.FoundryRollup = nil
			}
			rec.CurrentRoute = "ao-foundry"
			rec.CurrentPhase = "atlas_workgraph_ready"
			rec.ExactNextAction = atlasWorkgraphNextAction
			if rec.ExactNextAction == "" && counts.Ready > 0 {
				rec.ExactNextAction = "send first safe Atlas node to AO Foundry"
			}
			AppendRouteHistory(rec, routeFromRecord(*rec, "Atlas workgraph imported"))
			gate := EvaluateReturnGate(*rec)
			rec.ReturnGate = &gate
			reconciliation := BuildRouteReconciliation(*rec)
			rec.Reconciliation = &reconciliation
		case kind == "atlas-recommendation-readback":
			readback := parseAtlasRecommendationReadbackCounts(doc)
			foreignReadback := stringFromAny(doc["mission_id"]) != "" && stringFromAny(doc["mission_id"]) != missionID
			rec.Evidence.AtlasRecommendation = &readback
			rec.Evidence.AtlasWorkgraph = &NodeCounts{
				Total:     readback.TotalNodes,
				Ready:     readback.ReadyNodes,
				Completed: readback.CompletedNodes,
			}
			rec.ExactNextAction = readback.ExactNextAction
			switch {
			case !foreignReadback && atlasRecommendationReadbackClosesMission(readback):
				rec.Status = "done"
				rec.CurrentRoute = "complete"
				rec.CurrentPhase = "complete"
				rec.ExactNextAction = "mission complete; read final rollup and recommended next tasks"
			case atlasRecommendationReadbackTerminalBlocker(readback):
				rec.Status = "blocked"
				rec.CurrentRoute = "ao-atlas"
				rec.CurrentPhase = "atlas_recommendation_" + readback.Status
				blocker := atlasRecommendationBlocker(readback)
				rec.Blockers = appendMissingString(rec.Blockers, blocker)
				rec.ExactNextAction = "Atlas recommendation readback " + readback.Status + ": " + blocker
			default:
				rec.CurrentRoute = "ao-atlas"
				rec.CurrentPhase = "atlas_recommendation_readback_recorded"
				if foreignReadback {
					rec.ExactNextAction = "reconcile foreign Atlas recommendation readback before closing parent mission"
				} else if rec.ExactNextAction == "" {
					rec.ExactNextAction = "continue AO Atlas recommendation wave from latest ready node"
				}
			}
			AppendRouteHistory(rec, routeFromRecord(*rec, "Atlas recommendation readback imported"))
			gate := EvaluateReturnGate(*rec)
			rec.ReturnGate = &gate
			reconciliation := BuildRouteReconciliation(*rec)
			rec.Reconciliation = &reconciliation
		case kind == "atlas-final-synthesis-readback":
			readback := parseAtlasFinalSynthesisReadbackCounts(doc)
			if err := validateAtlasFinalSynthesisReadback(readback); err != nil {
				return err
			}
			parentBoundReadback := readback.MissionID == missionID
			rec.Evidence.AtlasFinalSynthesis = &readback
			rec.Evidence.AtlasRecommendation = atlasRecommendationFromFinalSynthesis(readback)
			rec.Evidence.AtlasWorkgraph = &NodeCounts{
				Total:     readback.TotalNodes,
				Ready:     readback.ReadyNodes,
				Blocked:   readback.BlockedNodes,
				Completed: readback.CompletedNodes,
			}
			rec.ExactNextAction = readback.ExactNextAction
			switch {
			case parentBoundReadback && atlasFinalSynthesisClosesMission(readback):
				rec.Status = "done"
				rec.CurrentRoute = "complete"
				rec.CurrentPhase = "complete"
				if rec.ExactNextAction == "" {
					rec.ExactNextAction = "mission complete; read final rollup and recommended next tasks"
				}
			case readback.Status == "blocked" || readback.Status == "denied":
				rec.Status = "blocked"
				rec.CurrentRoute = "ao-atlas"
				rec.CurrentPhase = "atlas_final_synthesis_" + readback.Status
				blocker := atlasFinalSynthesisBlocker(readback)
				rec.Blockers = appendMissingString(rec.Blockers, blocker)
				rec.ExactNextAction = "Atlas final synthesis readback " + readback.Status + ": " + blocker
			default:
				rec.CurrentRoute = "ao-atlas"
				rec.CurrentPhase = "atlas_final_synthesis_readback_recorded"
				if !parentBoundReadback {
					rec.ExactNextAction = "reconcile parent-bound Atlas final synthesis readback before closing mission"
				} else if rec.ExactNextAction == "" {
					rec.ExactNextAction = "continue AO Atlas final synthesis reconciliation from latest exact next action"
				}
			}
			AppendRouteHistory(rec, routeFromRecord(*rec, "Atlas final synthesis readback imported"))
			gate := EvaluateReturnGate(*rec)
			rec.ReturnGate = &gate
			reconciliation := BuildRouteReconciliation(*rec)
			rec.Reconciliation = &reconciliation
		case kind == "foundry-run-link":
			rec.CurrentPhase = "foundry_run_link_recorded"
			rec.ExactNextAction = "read next Atlas dependency-unblocked node or final rollup"
			AppendRouteHistory(rec, routeFromRecord(*rec, "Foundry run-link imported"))
			gate := EvaluateReturnGate(*rec)
			rec.ReturnGate = &gate
			reconciliation := BuildRouteReconciliation(*rec)
			rec.Reconciliation = &reconciliation
		case kind == "foundry-final-rollup":
			rollup := parseFoundryRollupCounts(doc)
			rec.Evidence.FoundryRollup = &rollup
			switch normalizeFoundryRollupStatus(rollup.Status) {
			case "completed", "promoted":
				if !foundryRollupClosesMission(rollup) {
					rec.CurrentPhase = "foundry_final_rollup_recorded"
					rec.ExactNextAction = "review final rollup node counts before closure"
					break
				}
				rec.Status = "done"
				rec.CurrentRoute = "complete"
				rec.CurrentPhase = "complete"
				rec.ExactNextAction = "mission complete; read final rollup and recommended next tasks"
			case "denied":
				rec.Status = "blocked"
				rec.CurrentRoute = "ao-atlas"
				rec.CurrentPhase = "foundry_rollup_denied"
				rec.ExactNextAction = "Foundry rollup denied; generate repair/repack support node through AO Atlas"
				rec.Blockers = appendMissingString(rec.Blockers, "foundry final rollup status denied")
			case "blocked":
				rec.Status = "blocked"
				rec.CurrentRoute = "ao-atlas"
				rec.CurrentPhase = "foundry_rollup_blocked"
				rec.ExactNextAction = "Foundry rollup blocked; resolve exact blocker before continuing"
				rec.Blockers = appendMissingString(rec.Blockers, "foundry final rollup status blocked")
			default:
				rec.CurrentPhase = "foundry_final_rollup_recorded"
				rec.ExactNextAction = "review final rollup blockers before continuing"
			}
			AppendRouteHistory(rec, routeFromRecord(*rec, "Foundry final rollup imported"))
			gate := EvaluateReturnGate(*rec)
			rec.ReturnGate = &gate
			reconciliation := BuildRouteReconciliation(*rec)
			rec.Reconciliation = &reconciliation
		case kind == "scheduler-readback":
			rec.Evidence.SchedulerReadback = &SchedulerEvidenceCounts{
				Status:          stringFromAny(doc["status"]),
				Scheduler:       stringFromAny(doc["scheduler"]),
				EventLoop:       boolFromAny(doc["event_loop"]),
				FreshnessStatus: classifyFreshness(stringFromAny(doc["generated_at_utc"])),
				ExecutesWork:    false,
			}
			rec.CurrentPhase = "scheduler_readback_recorded"
			rec.ExactNextAction = "scheduler wakeup readback recorded; continue mission through AO Mission event loop"
			AppendRouteHistory(rec, routeFromRecord(*rec, "Scheduler readback imported"))
		case kind == "scheduler-recovery-readback":
			rec.Evidence.SchedulerRecovery = &SchedulerRecoveryCounts{
				Status:        stringFromAny(doc["status"]),
				RecoveryMode:  stringFromAny(doc["recovery_mode"]),
				MissedWakeups: intFromAny(doc["missed_wakeups"]),
				ExecutesWork:  false,
			}
			rec.CurrentPhase = "scheduler_recovery_recorded"
			rec.ExactNextAction = stringFromAny(doc["exact_next_action"])
			if rec.ExactNextAction == "" {
				rec.ExactNextAction = "scheduler recovery readback recorded; continue mission through AO Mission event loop"
			}
			AppendRouteHistory(rec, routeFromRecord(*rec, "Scheduler recovery readback imported"))
		case kind == "ledger-compaction-readback":
			rec.Evidence.LedgerCompaction = &LedgerCompactionCounts{
				RouteHistoryBefore: intFromAny(doc["route_history_before"]),
				RouteHistoryAfter:  intFromAny(doc["route_history_after"]),
				StepsBefore:        intFromAny(doc["steps_before"]),
				StepsAfter:         intFromAny(doc["steps_after"]),
			}
			rec.CurrentPhase = "ledger_compaction_recorded"
			rec.ExactNextAction = "ledger compaction readback recorded; continue from retained route and step evidence"
			AppendRouteHistory(rec, routeFromRecord(*rec, "Ledger compaction readback imported"))
		case kind == aoNextCandidateImportKind:
			projection := *aoNextProjection
			projection.ArtifactDigest = ref.Digest
			projection.ContentRef = ref.ContentRef
			projection.OriginalRef = ref.Ref
			projection.GeneratedAtUTC = now(nil)
			rec.Evidence.AONextCandidate = &projection
		case kind == aoNextJournalPrefixImportKind:
			projection := *aoNextJournalProjection
			projection.ArtifactDigest = ref.Digest
			rec.Evidence.AONextJournalProjection = &projection
		default:
			return fmt.Errorf("unsupported import kind %q", kind)
		}
		if chainReference != nil {
			if err := recordCorrelationChainImport(rec, *chainReference, *correlatedBinding); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ImportReadback{}, err
	}
	return ImportReadback{
		Schema:                 "ao.mission.import-readback.v0.1",
		MissionID:              r.MissionID,
		CorrelationID:          r.CorrelationID,
		CorrelationChainDigest: chainDigest,
		Kind:                   kind,
		Status:                 "recorded",
		Artifact:               ref,
		ExactNextAction:        r.ExactNextAction,
		SafeToExecute:          false,
		ExecutesWork:           false,
		ApprovesWork:           false,
		GeneratedAtUTC:         now(nil),
	}, nil
}

func isMissionEvidenceReadback(kind string) bool {
	switch kind {
	case "atlas-recommendation-readback", "atlas-final-synthesis-readback", "scheduler-readback", "scheduler-recovery-readback", "ledger-compaction-readback", aoNextCandidateImportKind, aoNextJournalPrefixImportKind:
		return true
	default:
		return false
	}
}

func routeFromRecord(rec Record, reason string) RouteDecision {
	return RouteDecision{
		Schema:          RouteSchema,
		MissionID:       rec.MissionID,
		Route:           rec.CurrentRoute,
		Reason:          reason,
		SafeToRequest:   true,
		SafeToExecute:   false,
		SafeToPromote:   false,
		ExactNextAction: rec.ExactNextAction,
		GeneratedAtUTC:  now(nil),
	}
}

func classifyFreshness(generatedAt string) string {
	return classifyFreshnessAt(generatedAt, time.Now().UTC())
}

func classifyFreshnessAt(generatedAt string, evaluatedAt time.Time) string {
	if generatedAt == "" {
		return "unknown"
	}
	stamp, err := time.Parse(time.RFC3339, generatedAt)
	if err != nil {
		return "unknown"
	}
	age := evaluatedAt.Sub(stamp)
	if age > 24*time.Hour {
		return "stale"
	}
	return "fresh"
}

func digestBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func countWorkgraphNodes(doc map[string]any) NodeCounts {
	var counts NodeCounts
	nodes, _ := doc["nodes"].([]any)
	for _, node := range nodes {
		counts.Total++
		obj, _ := node.(map[string]any)
		status, _ := obj["status"].(string)
		switch status {
		case "ready":
			counts.Ready++
		case "blocked":
			counts.Blocked++
		case "completed", "complete", "done":
			counts.Completed++
		case "failed", "fail":
			counts.Failed++
		}
	}
	return counts
}

func firstReadyAtlasWorkgraphNode(doc map[string]any) (string, error) {
	nodes, _ := doc["nodes"].([]any)
	statuses := make(map[string]string, len(nodes))
	identities := make([]string, len(nodes))
	sawReadyWithIncompleteDependencies := false
	for index, node := range nodes {
		obj, _ := node.(map[string]any)
		identity, err := atlasWorkgraphNodeIdentity(obj)
		if err != nil {
			return "", fmt.Errorf("atlas-workgraph nodes[%d] %w", index, err)
		}
		identities[index] = identity
		if identity != "" {
			statuses[identity] = stringFromAny(obj["status"])
		}
	}
	for index, node := range nodes {
		obj, _ := node.(map[string]any)
		if stringFromAny(obj["status"]) != "ready" {
			continue
		}
		dependencies, err := atlasWorkgraphNodeDependencies(obj)
		if err != nil {
			return "", fmt.Errorf("atlas-workgraph nodes[%d] %w", index, err)
		}
		dependencyReady := true
		for _, dependency := range dependencies {
			status, ok := statuses[dependency]
			if !ok {
				return "", fmt.Errorf("atlas-workgraph nodes[%d] dependency %q is not a declared node", index, dependency)
			}
			if status != "completed" && status != "complete" && status != "done" {
				dependencyReady = false
				break
			}
		}
		if dependencyReady {
			return identities[index], nil
		}
		sawReadyWithIncompleteDependencies = true
	}
	if sawReadyWithIncompleteDependencies {
		return "", fmt.Errorf("has no dependency-ready node")
	}
	return "", nil
}

func atlasWorkgraphNodeIdentity(node map[string]any) (string, error) {
	id, idPresent, err := atlasWorkgraphNodeID(node, "id")
	if err != nil {
		return "", err
	}
	nodeID, nodeIDPresent, err := atlasWorkgraphNodeID(node, "node_id")
	if err != nil {
		return "", err
	}
	if idPresent && nodeIDPresent && id != nodeID {
		return "", fmt.Errorf("has conflicting id and node_id")
	}
	candidate := id
	if candidate == "" {
		candidate = nodeID
	}
	if candidate != "" && !atlasWorkgraphNodeIDPattern.MatchString(candidate) {
		return "", fmt.Errorf("identity must be a bounded ASCII identifier")
	}
	return candidate, nil
}

func atlasWorkgraphNodeDependencies(node map[string]any) ([]string, error) {
	value, present := node["dependencies"]
	if !present {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("dependencies must be an array")
	}
	dependencies := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		dependency, ok := value.(string)
		if !ok || !atlasWorkgraphNodeIDPattern.MatchString(dependency) {
			return nil, fmt.Errorf("dependencies must contain bounded ASCII identifiers")
		}
		if _, duplicate := seen[dependency]; duplicate {
			return nil, fmt.Errorf("dependencies must not contain duplicates")
		}
		seen[dependency] = struct{}{}
		dependencies = append(dependencies, dependency)
	}
	return dependencies, nil
}

func atlasWorkgraphNodeID(node map[string]any, field string) (string, bool, error) {
	value, present := node[field]
	if !present {
		return "", false, nil
	}
	id, ok := value.(string)
	if !ok || strings.TrimSpace(id) == "" {
		return "", true, fmt.Errorf("%s must be a non-empty string", field)
	}
	return id, true, nil
}

func parseFoundryRollupCounts(doc map[string]any) FoundryRollupCounts {
	status, _ := doc["status"].(string)
	return FoundryRollupCounts{
		Status:         normalizeFoundryRollupStatus(status),
		CompletedNodes: intFromAny(doc["completed_nodes"]),
		TotalNodes:     intFromAny(doc["total_nodes"]),
	}
}

func parseAtlasRecommendationReadbackCounts(doc map[string]any) AtlasRecommendationReadbackCounts {
	return AtlasRecommendationReadbackCounts{
		Status:               stringFromAny(doc["status"]),
		TotalNodes:           intFromAny(doc["total_nodes"]),
		CompletedNodes:       intFromAny(doc["completed_nodes"]),
		ReadyNodes:           intFromAny(doc["ready_nodes"]),
		CheckpointCount:      intFromAny(doc["checkpoint_count"]),
		ElapsedMinutes:       intFromAny(doc["elapsed_minutes"]),
		MinMinutesMet:        boolFromAny(doc["min_minutes_met"]),
		LeaseTimeStatus:      stringFromAny(doc["lease_time_status"]),
		ReturnGateStatus:     stringFromAny(doc["return_gate_status"]),
		FinalResponseAllowed: boolFromAny(doc["final_response_allowed"]),
		Blocker:              stringFromAny(doc["blocker"]),
		RSIRemainsDenied:     boolFromAny(doc["rsi_remains_denied"]),
		ExactNextAction:      stringFromAny(doc["exact_next_action"]),
	}
}

func parseAtlasFinalSynthesisReadbackCounts(doc map[string]any) AtlasFinalSynthesisReadbackCounts {
	return AtlasFinalSynthesisReadbackCounts{
		MissionID:            stringFromAny(doc["mission_id"]),
		CorrelationID:        stringFromAny(doc["correlation_id"]),
		ContractVersion:      stringFromAny(doc["contract_version"]),
		Status:               stringFromAny(doc["status"]),
		TotalNodes:           intFromAny(doc["total_nodes"]),
		CompletedNodes:       intFromAny(doc["completed_nodes"]),
		ReadyNodes:           intFromAny(doc["ready_nodes"]),
		BlockedNodes:         intFromAny(doc["blocked_nodes"]),
		MinimumNodes:         intFromAny(doc["minimum_nodes"]),
		ReturnGateStatus:     stringFromAny(doc["return_gate_status"]),
		FinalResponseAllowed: boolFromAny(doc["final_response_allowed"]),
		FinalResponseReason:  stringFromAny(doc["final_response_reason"]),
		AtlasWorkgraphStatus: stringFromAny(doc["atlas_workgraph_status"]),
		FoundryRollup:        stringFromAny(doc["foundry_rollup"]),
		PromoterStatus:       stringFromAny(doc["promoter_status"]),
		CommandReadback:      stringFromAny(doc["command_readback"]),
		EventSearchBound:     boolFromAny(doc["event_search_bound"]),
		BranchCleanupBound:   boolFromAny(doc["branch_cleanup_bound"]),
		RSIRemainsDenied:     boolFromAny(doc["rsi_remains_denied"]),
		ExactNextAction:      stringFromAny(doc["exact_next_action"]),
	}
}

func validateAtlasFinalSynthesisReadback(readback AtlasFinalSynthesisReadbackCounts) error {
	switch {
	case readback.ContractVersion != "ao.atlas.ao-mission-final-synthesis-readback.v0.1":
		return fmt.Errorf("contract_version must be ao.atlas.ao-mission-final-synthesis-readback.v0.1")
	case readback.TotalNodes != readback.CompletedNodes+readback.ReadyNodes+readback.BlockedNodes:
		return fmt.Errorf("total_nodes must equal completed_nodes plus ready_nodes plus blocked_nodes")
	case readback.FinalResponseAllowed && readback.ReadyNodes > 0:
		return fmt.Errorf("final response cannot be allowed while ready nodes remain")
	case readback.FinalResponseAllowed && readback.BlockedNodes > 0:
		return fmt.Errorf("final response cannot be allowed while blocked nodes remain")
	case readback.FinalResponseAllowed && readback.CompletedNodes < readback.MinimumNodes:
		return fmt.Errorf("final response requires completed_nodes to meet minimum_nodes")
	case readback.FinalResponseAllowed && readback.ReturnGateStatus != "final_response_allowed":
		return fmt.Errorf("final response requires return_gate_status final_response_allowed")
	case readback.FinalResponseAllowed && readback.Status != "completed":
		return fmt.Errorf("final response requires completed status")
	case readback.FinalResponseAllowed && readback.AtlasWorkgraphStatus != "completed":
		return fmt.Errorf("final response requires completed Atlas workgraph status")
	case readback.FinalResponseAllowed && readback.CommandReadback != "ready":
		return fmt.Errorf("final response requires ready command_readback")
	case readback.FinalResponseAllowed && !readback.EventSearchBound:
		return fmt.Errorf("final response requires event search binding")
	case readback.FinalResponseAllowed && !readback.BranchCleanupBound:
		return fmt.Errorf("final response requires branch cleanup binding")
	case !readback.RSIRemainsDenied:
		return fmt.Errorf("rsi_remains_denied must be true")
	default:
		return nil
	}
}

func atlasRecommendationFromFinalSynthesis(readback AtlasFinalSynthesisReadbackCounts) *AtlasRecommendationReadbackCounts {
	leaseStatus := "minimum_minutes_unmet"
	minMinutesMet := false
	checkpoints := 0
	if readback.FinalResponseAllowed {
		leaseStatus = "minimum_minutes_met"
		minMinutesMet = true
		checkpoints = readback.TotalNodes
	}
	return &AtlasRecommendationReadbackCounts{
		Status:               readback.Status,
		TotalNodes:           readback.TotalNodes,
		CompletedNodes:       readback.CompletedNodes,
		ReadyNodes:           readback.ReadyNodes,
		CheckpointCount:      checkpoints,
		MinMinutesMet:        minMinutesMet,
		LeaseTimeStatus:      leaseStatus,
		ReturnGateStatus:     readback.ReturnGateStatus,
		FinalResponseAllowed: readback.FinalResponseAllowed,
		Blocker:              atlasFinalSynthesisBlocker(readback),
		RSIRemainsDenied:     readback.RSIRemainsDenied,
		ExactNextAction:      readback.ExactNextAction,
	}
}

func atlasRecommendationReadbackClosesMission(readback AtlasRecommendationReadbackCounts) bool {
	return readback.Status == "completed" &&
		readback.TotalNodes > 0 &&
		readback.CompletedNodes == readback.TotalNodes &&
		readback.ReadyNodes == 0 &&
		readback.CheckpointCount >= readback.TotalNodes &&
		readback.MinMinutesMet &&
		readback.LeaseTimeStatus == "minimum_minutes_met" &&
		readback.ReturnGateStatus == "final_response_allowed" &&
		readback.FinalResponseAllowed
}

func atlasFinalSynthesisClosesMission(readback AtlasFinalSynthesisReadbackCounts) bool {
	return readback.Status == "completed" &&
		readback.TotalNodes > 0 &&
		readback.CompletedNodes == readback.TotalNodes &&
		readback.ReadyNodes == 0 &&
		readback.BlockedNodes == 0 &&
		readback.CompletedNodes >= readback.MinimumNodes &&
		readback.ReturnGateStatus == "final_response_allowed" &&
		readback.FinalResponseAllowed &&
		readback.AtlasWorkgraphStatus == "completed" &&
		readback.CommandReadback == "ready" &&
		readback.PromoterStatus != "" &&
		readback.EventSearchBound &&
		readback.BranchCleanupBound &&
		readback.RSIRemainsDenied
}

func atlasFinalSynthesisBlocker(readback AtlasFinalSynthesisReadbackCounts) string {
	switch {
	case readback.FinalResponseAllowed && readback.ReadyNodes > 0:
		return "final response cannot be allowed while ready nodes remain"
	case readback.FinalResponseAllowed && readback.BlockedNodes > 0:
		return "final response cannot be allowed while blocked nodes remain"
	case readback.CompletedNodes < readback.MinimumNodes:
		return "minimum nodes unmet"
	case readback.ReturnGateStatus != "" && readback.ReturnGateStatus != "final_response_allowed":
		return "return gate status " + readback.ReturnGateStatus
	case readback.CommandReadback != "" && readback.CommandReadback != "ready":
		return "command readback status " + readback.CommandReadback
	case !readback.RSIRemainsDenied:
		return "RSI denial evidence missing"
	default:
		return ""
	}
}

func atlasRecommendationReadbackTerminalBlocker(readback AtlasRecommendationReadbackCounts) bool {
	return readback.Status == "denied" || readback.Status == "blocked"
}

func atlasRecommendationBlocker(readback AtlasRecommendationReadbackCounts) string {
	if readback.Blocker != "" {
		return readback.Blocker
	}
	if readback.ReturnGateStatus != "" {
		return "return gate status " + readback.ReturnGateStatus
	}
	return "terminal Atlas recommendation status " + readback.Status
}

func appendMissingString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func stringFromAny(v any) string {
	s, _ := v.(string)
	return s
}

func validateCorrelatedAtlasWorkgraphIdentity(existing Record, kind string, doc map[string]any) error {
	if kind != "atlas-workgraph" || existing.CorrelationID == "" {
		return nil
	}
	missionID := stringFromAny(doc["mission_id"])
	if missionID == "" {
		return fmt.Errorf("atlas-workgraph mission_id is required for correlated mission")
	}
	if missionID != existing.MissionID {
		return fmt.Errorf("atlas-workgraph mission_id does not match mission")
	}
	targetInstance := stringFromAny(doc["target_instance"])
	if targetInstance == "" {
		return fmt.Errorf("atlas-workgraph target_instance is required for correlated mission")
	}
	if targetInstance != existing.MissionID {
		return fmt.Errorf("atlas-workgraph target_instance does not match mission")
	}
	return nil
}

func boolFromAny(v any) bool {
	b, _ := v.(bool)
	return b
}
