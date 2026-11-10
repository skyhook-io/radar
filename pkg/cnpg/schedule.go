package cnpg

import "github.com/robfig/cron"

// ParseSchedule uses the operator's parser: v1's six-field, seconds-first
// grammar. v3 differs for stepped day-of-month combined with day-of-week.
func ParseSchedule(spec string) (cron.Schedule, error) { return cron.Parse(spec) }
