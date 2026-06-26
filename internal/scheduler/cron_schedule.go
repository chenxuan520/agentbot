package scheduler

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cronlib "github.com/robfig/cron/v3"
)

type cronPayload struct {
	CronExpr string `json:"cron"`
	Timezone string `json:"timezone"`
}

func FirstRunAt(runAtText, cronExpr, timezone string, now time.Time) (time.Time, error) {
	hasRunAt := strings.TrimSpace(runAtText) != ""
	hasCron := strings.TrimSpace(cronExpr) != ""

	switch {
	case hasRunAt && hasCron:
		return time.Time{}, fmt.Errorf("schedule requires either runAt or cron, not both")
	case !hasRunAt && !hasCron:
		return time.Time{}, fmt.Errorf("schedule requires runAt or cron")
	case hasRunAt:
		runAt, err := time.Parse(time.RFC3339, runAtText)
		if err != nil {
			return time.Time{}, err
		}
		return runAt.UTC(), nil
	default:
		if strings.TrimSpace(timezone) == "" {
			return time.Time{}, fmt.Errorf("cron schedule requires timezone")
		}
		return nextCronRunAt(cronExpr, timezone, now.UTC())
	}
}

func ApplyCronPayload(payload map[string]any, cronExpr, timezone string) map[string]any {
	result := clonePayloadMap(payload)
	if result == nil {
		result = map[string]any{}
	}
	if strings.TrimSpace(cronExpr) == "" {
		delete(result, "cron")
		delete(result, "timezone")
		return result
	}
	result["cron"] = strings.TrimSpace(cronExpr)
	result["timezone"] = strings.TrimSpace(timezone)
	return result
}

func NextRunAt(job Job, triggeredAt time.Time) (time.Time, bool, error) {
	var payload cronPayload
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return time.Time{}, false, err
	}
	if strings.TrimSpace(payload.CronExpr) == "" {
		return time.Time{}, false, nil
	}
	next, err := nextCronRunAt(payload.CronExpr, payload.Timezone, triggeredAt.UTC())
	if err != nil {
		return time.Time{}, false, err
	}
	return next, true, nil
}

func nextCronRunAt(cronExpr, timezone string, after time.Time) (time.Time, error) {
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return time.Time{}, err
	}
	schedule, err := cronlib.ParseStandard(strings.TrimSpace(cronExpr))
	if err != nil {
		return time.Time{}, err
	}
	next := schedule.Next(after.In(location))
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron schedule returned zero next run time")
	}
	return next.UTC(), nil
}
