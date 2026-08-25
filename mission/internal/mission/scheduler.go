package mission

import (
	"os/exec"
)

func ScheduleReadback(missionID string, every string, eventLoop bool) SchedulerReadback {
	if _, err := exec.LookPath("codex-cron"); err != nil {
		return SchedulerReadback{Schema: SchedulerReadbackSchema, MissionID: missionID, Status: "blocked", Scheduler: "codex-cron", EventLoop: eventLoop, Reason: "codex-cron binary missing; scheduled continuation fails closed", GeneratedAtUTC: now(nil)}
	}
	return SchedulerReadback{Schema: SchedulerReadbackSchema, MissionID: missionID, Status: "ready", Scheduler: "codex-cron", EventLoop: eventLoop, Reason: "scheduler adapter ready; codex-cron remains wakeup substrate only", GeneratedAtUTC: now(nil)}
}
