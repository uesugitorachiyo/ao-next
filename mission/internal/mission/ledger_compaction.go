package mission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func CompactMissionLedger(s Store, missionID string, opts LedgerCompactionOptions) (LedgerCompactionReadback, error) {
	if opts.KeepRouteHistory < 1 {
		return LedgerCompactionReadback{}, fmt.Errorf("keep route history must be at least 1")
	}
	if opts.KeepSteps < 1 {
		return LedgerCompactionReadback{}, fmt.Errorf("keep steps must be at least 1")
	}

	if opts.DryRun {
		rec, err := s.Load(missionID)
		if err != nil {
			return LedgerCompactionReadback{}, err
		}
		beforeRoutes := len(rec.RouteHistory)
		beforeSteps := len(rec.Steps)
		afterRoutes := beforeRoutes
		if afterRoutes > opts.KeepRouteHistory {
			afterRoutes = opts.KeepRouteHistory
		}
		afterSteps := beforeSteps
		if afterSteps > opts.KeepSteps {
			afterSteps = opts.KeepSteps
		}
		return LedgerCompactionReadback{
			Schema:              "ao.mission.ledger-compaction-readback.v0.1",
			MissionID:           rec.MissionID,
			CorrelationID:       rec.CorrelationID,
			Status:              "dry_run",
			RouteHistoryBefore:  beforeRoutes,
			RouteHistoryAfter:   afterRoutes,
			StepsBefore:         beforeSteps,
			StepsAfter:          afterSteps,
			ExactNextAction:     "mission ledger compaction dry-run recorded; rerun without --dry-run to compact retained readbacks",
			ExecutesWork:        false,
			ApprovesWork:        false,
			MutatesRepositories: false,
			GeneratedAtUTC:      now(s.Clock),
		}, nil
	}

	var readback LedgerCompactionReadback
	rec, err := s.Update(missionID, func(r *Record) error {
		beforeRoutes := len(r.RouteHistory)
		beforeSteps := len(r.Steps)
		if beforeRoutes > opts.KeepRouteHistory {
			r.RouteHistory = append([]RouteDecision(nil), r.RouteHistory[beforeRoutes-opts.KeepRouteHistory:]...)
		}
		if beforeSteps > opts.KeepSteps {
			r.Steps = append([]ContinuationStep(nil), r.Steps[beforeSteps-opts.KeepSteps:]...)
		}
		counts := LedgerCompactionCounts{
			RouteHistoryBefore: beforeRoutes,
			RouteHistoryAfter:  len(r.RouteHistory),
			StepsBefore:        beforeSteps,
			StepsAfter:         len(r.Steps),
		}
		r.Evidence.LedgerCompaction = &counts
		readback = LedgerCompactionReadback{
			Schema:              "ao.mission.ledger-compaction-readback.v0.1",
			MissionID:           r.MissionID,
			CorrelationID:       r.CorrelationID,
			Status:              "compacted",
			RouteHistoryBefore:  counts.RouteHistoryBefore,
			RouteHistoryAfter:   counts.RouteHistoryAfter,
			StepsBefore:         counts.StepsBefore,
			StepsAfter:          counts.StepsAfter,
			ExactNextAction:     r.ExactNextAction,
			ExecutesWork:        false,
			ApprovesWork:        false,
			MutatesRepositories: false,
			GeneratedAtUTC:      now(s.Clock),
		}
		return nil
	})
	if err != nil {
		return LedgerCompactionReadback{}, err
	}
	if readback.MissionID == "" {
		return LedgerCompactionReadback{}, fmt.Errorf("ledger compaction failed for mission %s", rec.MissionID)
	}
	return readback, nil
}

func CompactMissionTimeline(s Store, missionID string, opts LedgerCompactionOptions) (TimelineCompactionReadback, error) {
	ledger, err := CompactMissionLedger(s, missionID, opts)
	if err != nil {
		return TimelineCompactionReadback{}, err
	}
	rec, err := s.Load(missionID)
	if err != nil {
		return TimelineCompactionReadback{}, err
	}
	timeline := struct {
		MissionID    string             `json:"mission_id"`
		RouteHistory []RouteDecision    `json:"route_history"`
		Steps        []ContinuationStep `json:"steps"`
	}{
		MissionID:    rec.MissionID,
		RouteHistory: rec.RouteHistory,
		Steps:        rec.Steps,
	}
	body, err := json.Marshal(timeline)
	if err != nil {
		return TimelineCompactionReadback{}, err
	}
	sum := sha256.Sum256(body)
	return TimelineCompactionReadback{
		Schema:              "ao.mission.timeline-compaction-readback.v0.1",
		MissionID:           ledger.MissionID,
		CorrelationID:       rec.CorrelationID,
		Status:              ledger.Status,
		RouteHistoryBefore:  ledger.RouteHistoryBefore,
		RouteHistoryAfter:   ledger.RouteHistoryAfter,
		StepsBefore:         ledger.StepsBefore,
		StepsAfter:          ledger.StepsAfter,
		TimelineDigest:      "sha256:" + hex.EncodeToString(sum[:]),
		ExactNextAction:     "mission timeline compacted and digest-bound; continue from retained route and step readbacks",
		ExecutesWork:        false,
		ApprovesWork:        false,
		MutatesRepositories: false,
		GeneratedAtUTC:      now(s.Clock),
	}, nil
}
