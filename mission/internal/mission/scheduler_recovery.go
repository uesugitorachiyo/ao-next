package mission

func BuildSchedulerRecoveryReadback(missionID string, replay SchedulerReplayReadback) SchedulerRecoveryReadback {
	missed := replay.Stale + replay.Unknown
	readback := SchedulerRecoveryReadback{
		Schema:          "ao.mission.scheduler-recovery-readback.v0.1",
		MissionID:       missionID,
		Status:          "ready",
		RecoveryMode:    "none_required",
		MissedWakeups:   missed,
		Fresh:           replay.Fresh,
		Stale:           replay.Stale,
		Unknown:         replay.Unknown,
		ExactNextAction: "scheduler wakeups fresh; continue normal mission routing",
		ExecutesWork:    false,
		ApprovesWork:    false,
		GeneratedAtUTC:  now(nil),
	}
	if missed > 0 {
		readback.Status = "attention_required"
		readback.RecoveryMode = "immediate_continue_recommended"
		readback.ExactNextAction = "ao-mission continue --mission " + missionID + " --until-done --max-iterations 1"
	}
	return readback
}
