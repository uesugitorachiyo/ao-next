package mission

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func ReplaySchedulerReadbacks(path string) (SchedulerReplayReadback, error) {
	var fixture struct {
		Schema         string `json:"schema"`
		EvaluatedAtUTC string `json:"evaluated_at_utc"`
		Readbacks      []struct {
			Schema         string `json:"schema"`
			MissionID      string `json:"mission_id"`
			Status         string `json:"status"`
			Scheduler      string `json:"scheduler"`
			EventLoop      bool   `json:"event_loop"`
			GeneratedAtUTC string `json:"generated_at_utc"`
			ExecutesWork   bool   `json:"executes_work"`
			ApprovesWork   bool   `json:"approves_work"`
		} `json:"readbacks"`
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return SchedulerReplayReadback{}, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		return SchedulerReplayReadback{}, err
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		return SchedulerReplayReadback{}, err
	}
	readback := SchedulerReplayReadback{
		Schema:         "ao.mission.scheduler-replay-readback.v0.1",
		Status:         "ready",
		ExecutesWork:   false,
		ApprovesWork:   false,
		EvaluatedAtUTC: fixture.EvaluatedAtUTC,
		GeneratedAtUTC: now(nil),
	}
	evaluatedAt := time.Now().UTC()
	if fixture.EvaluatedAtUTC != "" {
		parsed, err := time.Parse(time.RFC3339, fixture.EvaluatedAtUTC)
		if err != nil {
			return SchedulerReplayReadback{}, fmt.Errorf("scheduler replay evaluated_at_utc must be RFC3339")
		}
		evaluatedAt = parsed.UTC()
	}
	for _, item := range fixture.Readbacks {
		if item.Schema != SchedulerReadbackSchema {
			return SchedulerReplayReadback{}, fmt.Errorf("scheduler replay item schema must be %s", SchedulerReadbackSchema)
		}
		if item.ExecutesWork || item.ApprovesWork {
			return SchedulerReplayReadback{}, fmt.Errorf("scheduler replay item must not claim execution or approval authority")
		}
		readback.Total++
		switch classifyFreshnessAt(item.GeneratedAtUTC, evaluatedAt) {
		case "fresh":
			readback.Fresh++
		case "stale":
			readback.Stale++
		default:
			readback.Unknown++
		}
	}
	return readback, nil
}

func BuildSchedulerAlertSummary(replay SchedulerReplayReadback) SchedulerAlertSummary {
	summary := SchedulerAlertSummary{
		Schema:         "ao.mission.scheduler-alert-summary.v0.1",
		Status:         "ready",
		Total:          replay.Total,
		Fresh:          replay.Fresh,
		Stale:          replay.Stale,
		Unknown:        replay.Unknown,
		Alerts:         []string{},
		ExecutesWork:   false,
		ApprovesWork:   false,
		GeneratedAtUTC: now(nil),
	}
	if replay.Stale > 0 {
		summary.Alerts = append(summary.Alerts, fmt.Sprintf("%d scheduler readback(s) are stale", replay.Stale))
	}
	if replay.Unknown > 0 {
		summary.Alerts = append(summary.Alerts, fmt.Sprintf("%d scheduler readback(s) have unknown freshness", replay.Unknown))
	}
	if len(summary.Alerts) > 0 {
		summary.Status = "attention_required"
	}
	return summary
}
