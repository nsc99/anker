package exporter

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Stats struct {
	ReviewsToday     int      `json:"reviews_today"`
	StudyTimeSeconds float64  `json:"study_time_seconds"`
	StudyStreak      int      `json:"study_streak"`
	RetentionPercent *float64 `json:"retention_percent"`
	StudyDay         string   `json:"study_day"`
	RolloverHour     int      `json:"rollover_hour"`
	Timezone         string   `json:"timezone"`
}

type Review struct {
	Timestamp                int64
	Ease, Kind, Milliseconds int
}
type Collection struct {
	Reviews  []Review
	Rollover int
}

func ReadCollection(ctx context.Context, path string) (*Collection, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return nil, fmt.Errorf("check collection: %w", err)
	}
	if integrity != "ok" {
		return nil, fmt.Errorf("collection integrity check failed")
	}
	var schema int
	var conf string
	var created int64
	if err := db.QueryRowContext(ctx, "SELECT ver, conf, crt FROM col").Scan(&schema, &conf, &created); err != nil {
		return nil, err
	}
	c := &Collection{Rollover: 4}
	var rollover json.RawMessage
	if schema >= 14 {
		err = db.QueryRowContext(ctx, "SELECT val FROM config WHERE key = 'rollover'").Scan(&rollover)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
	} else {
		var config map[string]json.RawMessage
		if err := json.Unmarshal([]byte(conf), &config); err != nil {
			return nil, err
		}
		rollover = config["rollover"]
		// The legacy scheduler stores its rollover hour in the creation timestamp.
		if len(rollover) == 0 {
			c.Rollover = time.Unix(created, 0).UTC().Hour()
		}
	}
	if len(rollover) > 0 {
		if err := json.Unmarshal(rollover, &c.Rollover); err != nil || c.Rollover < 0 || c.Rollover > 23 {
			return nil, fmt.Errorf("invalid collection rollover")
		}
	}
	rows, err := db.QueryContext(ctx, "SELECT id, ease, type, time FROM revlog WHERE ease BETWEEN 1 AND 4 AND type BETWEEN 0 AND 3 ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Review
		if err := rows.Scan(&r.Timestamp, &r.Ease, &r.Kind, &r.Milliseconds); err != nil {
			return nil, err
		}
		c.Reviews = append(c.Reviews, r)
	}
	return c, rows.Err()
}

// Calendar comparisons avoid subtracting 24 hours across DST transitions.
func studyDay(t time.Time, loc *time.Location, rollover int) time.Time {
	local := t.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if local.Hour() < rollover {
		day = day.AddDate(0, 0, -1)
	}
	return day
}

func (c *Collection) Stats(now time.Time, loc *time.Location) Stats {
	today := studyDay(now, loc, c.Rollover)
	result := Stats{StudyDay: today.Format(time.DateOnly), RolloverHour: c.Rollover, Timezone: loc.String()}
	days := make(map[time.Time]bool)
	passed, total := 0, 0
	for _, r := range c.Reviews {
		if r.Timestamp > now.UnixMilli() {
			continue
		}
		day := studyDay(time.UnixMilli(r.Timestamp), loc, c.Rollover)
		days[day] = true
		if day.Equal(today) {
			result.ReviewsToday++
			if r.Milliseconds > 0 {
				result.StudyTimeSeconds += float64(r.Milliseconds) / 1000
			}
			// Today's scheduled review answers; learning/relearning and filtered
			// cram events are excluded from retention.
			if r.Kind == 1 {
				total++
				if r.Ease > 1 {
					passed++
				}
			}
		}
	}
	if total > 0 {
		percentage := 100 * float64(passed) / float64(total)
		result.RetentionPercent = &percentage
	}
	cursor := today
	if !days[cursor] {
		cursor = cursor.AddDate(0, 0, -1)
	}
	for days[cursor] {
		result.StudyStreak++
		cursor = cursor.AddDate(0, 0, -1)
	}
	return result
}
