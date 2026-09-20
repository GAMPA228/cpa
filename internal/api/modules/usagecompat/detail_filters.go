package usagecompat

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Detail intervals are [start_time, end_time), with explicit timezone offsets.
func parseDetailFilters(c *gin.Context, query *DetailPageQuery) error {
	switch strings.TrimSpace(c.Query("result")) {
	case "", "all":
	case "success":
		failed := false
		query.Failed = &failed
	case "failed":
		failed := true
		query.Failed = &failed
	default:
		return fmt.Errorf("result must be all, success or failed")
	}
	for _, bound := range []struct {
		name   string
		target *time.Time
	}{{"start_time", &query.StartTime}, {"end_time", &query.EndTime}} {
		raw := strings.TrimSpace(c.Query(bound.name))
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return fmt.Errorf("%s must be an RFC3339 timestamp with timezone", bound.name)
		}
		// SQLite stores nanoseconds; reject dates that would overflow UnixNano.
		if !time.Unix(0, parsed.UnixNano()).Equal(parsed) {
			return fmt.Errorf("%s is outside the supported timestamp range", bound.name)
		}
		*bound.target = parsed.UTC()
	}
	if !query.StartTime.IsZero() && !query.EndTime.IsZero() && !query.StartTime.Before(query.EndTime) {
		return fmt.Errorf("end_time must be later than start_time")
	}
	return nil
}
