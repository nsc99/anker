package exporter

import "time"

// ActivityDay is one cell in a study activity grid. Date is a study-day label
// in the configured timezone, rather than a UTC timestamp.
type ActivityDay struct {
	Date             string  `json:"date"`
	Reviews          int     `json:"reviews"`
	StudyTimeSeconds float64 `json:"study_time_seconds"`
}

type Activity struct {
	Days         []ActivityDay `json:"days"`
	StudyDay     string        `json:"study_day"`
	RolloverHour int           `json:"rollover_hour"`
	Timezone     string        `json:"timezone"`
	StudyStreak  int           `json:"study_streak"`
}

// Activity returns count consecutive study days ending today, oldest first.
// Empty days are included so a visualization can render gaps without guessing.
// Callers must provide a positive count.
func (c *Collection) Activity(now time.Time, loc *time.Location, count int) Activity {
	today := studyDay(now, loc, c.Rollover)
	first := today.AddDate(0, 0, 1-count)
	result := Activity{
		Days:         make([]ActivityDay, count),
		StudyDay:     today.Format(time.DateOnly),
		RolloverHour: c.Rollover,
		Timezone:     loc.String(),
		StudyStreak:  c.Stats(now, loc).StudyStreak,
	}
	for i := range result.Days {
		result.Days[i].Date = first.AddDate(0, 0, i).Format(time.DateOnly)
	}
	for _, review := range c.Reviews {
		if review.Timestamp > now.UnixMilli() {
			continue
		}
		day := studyDay(time.UnixMilli(review.Timestamp), loc, c.Rollover)
		if day.Before(first) || day.After(today) {
			continue
		}
		// studyDay produces calendar labels at UTC midnight, so this difference
		// is safe even when the actual local study day spans 23 or 25 hours.
		index := int(day.Sub(first) / (24 * time.Hour))
		result.Days[index].Reviews++
		if review.Milliseconds > 0 {
			result.Days[index].StudyTimeSeconds += float64(review.Milliseconds) / 1000
		}
	}
	return result
}

type ActivityResponse struct {
	Activity
	LastSuccessfulSync time.Time `json:"last_successful_sync"`
	Stale              bool      `json:"stale"`
	SyncOK             bool      `json:"sync_ok"`
}
