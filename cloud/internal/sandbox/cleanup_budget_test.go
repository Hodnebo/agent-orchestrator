package sandbox

import (
	"testing"
	"time"
)

// A Cleanup run gets the sum of its steps' time limits, each with its grace,
// at least the default 10 minutes and at most 2 hours; of several lists that
// may run, the largest.
func TestCleanupBudgetCoversItsStepsTimeLimits(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		lists [][]string
		want  time.Duration
	}{
		"none":              {nil, 10 * time.Minute},
		"blank":             {[][]string{{" "}}, 10 * time.Minute},
		"one short step":    {[][]string{{"# ao-timeout: 1m\nmake down"}}, 10 * time.Minute},
		"one default step":  {[][]string{{"make down"}}, 10*time.Minute + 10*time.Second},
		"two default steps": {[][]string{{"make down", "docker compose down"}}, 20*time.Minute + 20*time.Second},
		"long steps":        {[][]string{{"# ao-timeout: 45m\na", "# ao-timeout: 30m\nb"}}, 75*time.Minute + 20*time.Second},
		"capped":            {[][]string{{"# ao-timeout: 120m\na", "# ao-timeout: 90m\nb"}}, 2 * time.Hour},
		"largest list":      {[][]string{{"make down"}, {"# ao-timeout: 60m\nmake down"}}, time.Hour + 10*time.Second},
	} {
		if got := CleanupBudget(test.lists...); got != test.want {
			t.Errorf("%s: budget = %s, want %s", name, got, test.want)
		}
	}
}
