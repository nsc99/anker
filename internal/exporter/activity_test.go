package exporter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestActivityRolloverAndGaps(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 30, 3, 0, 0, 0, loc)
	review := func(day, hour, milliseconds int) Review {
		return Review{Timestamp: time.Date(2026, 3, day, hour, 0, 0, 0, loc).UnixMilli(), Ease: 3, Kind: 1, Milliseconds: milliseconds}
	}
	col := Collection{Rollover: 4, Reviews: []Review{
		review(26, 5, 1000), // outside the requested range
		review(27, 5, 1500),
		review(28, 3, 500), // still March 27
		review(29, 5, 2000),
		review(30, 2, -100), // still March 29, after the DST change
		review(30, 5, 1000), // future answer
		{Timestamp: now.Add(30 * time.Minute).UnixMilli(), Ease: 3, Kind: 1, Milliseconds: 1000}, // future answer in today's study day
	}}
	got := col.Activity(now, loc, 3)
	if got.StudyDay != "2026-03-29" || got.RolloverHour != 4 || got.Timezone != "Europe/Berlin" || got.StudyStreak != 1 {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	want := []ActivityDay{{Date: "2026-03-27", Reviews: 2, StudyTimeSeconds: 2}, {Date: "2026-03-28"}, {Date: "2026-03-29", Reviews: 2, StudyTimeSeconds: 2}}
	for i := range want {
		if got.Days[i] != want[i] {
			t.Errorf("day %d: got %+v, want %+v", i, got.Days[i], want[i])
		}
	}
	stats := col.Stats(now, loc)
	last := got.Days[len(got.Days)-1]
	if last.Reviews != stats.ReviewsToday || last.StudyTimeSeconds != stats.StudyTimeSeconds {
		t.Fatal("activity and today's statistics disagree")
	}
}

func TestActivityCalendarBoundaries(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                string
		now                 time.Time
		first, middle, last string
	}{
		{"leap day", time.Date(2024, 3, 1, 12, 0, 0, 0, loc), "2024-02-28", "2024-02-29", "2024-03-01"},
		{"autumn DST", time.Date(2026, 10, 26, 12, 0, 0, 0, loc), "2026-10-24", "2026-10-25", "2026-10-26"},
		{"year boundary", time.Date(2026, 1, 1, 12, 0, 0, 0, loc), "2025-12-30", "2025-12-31", "2026-01-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			col := Collection{Rollover: 4, Reviews: []Review{
				{Timestamp: tc.now.AddDate(0, 0, -1).UnixMilli(), Ease: 3, Kind: 1, Milliseconds: 1000},
			}}
			got := col.Activity(tc.now, loc, 3)
			if got.Days[0].Date != tc.first || got.Days[1].Date != tc.middle || got.Days[2].Date != tc.last || got.Days[1].Reviews != 1 || got.StudyStreak != 1 {
				t.Fatalf("unexpected calendar days: %+v", got)
			}
		})
	}
}

func TestActivityStreakExtendsBeforeWindow(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	col := Collection{Rollover: 4}
	for day := 0; day < 3; day++ {
		col.Reviews = append(col.Reviews, Review{Timestamp: now.AddDate(0, 0, -day).UnixMilli(), Ease: 3, Kind: 1})
	}
	got := col.Activity(now, time.UTC, 1)
	if len(got.Days) != 1 || got.Days[0].Reviews != 1 || got.StudyStreak != 3 {
		t.Fatalf("window truncated the streak: %+v", got)
	}
}

func TestActivityHandler(t *testing.T) {
	now := time.Now().UTC()
	service := NewService(Config{Location: time.UTC, StaleAfter: time.Hour})
	handler := service.Handler()
	get := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/activity"+query, nil))
		if result.Header().Get("Cache-Control") != "no-store" || result.Header().Get("Content-Type") != "application/json" {
			t.Fatal("missing JSON or cache headers")
		}
		return result
	}
	if got := get(""); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty snapshot: status %d", got.Code)
	}
	service.collection = &Collection{Rollover: 4, Reviews: []Review{{Timestamp: now.Add(-time.Hour).UnixMilli(), Ease: 3, Kind: 1, Milliseconds: 1000}}}
	service.lastSuccess = now
	for _, days := range []int{0, 1, 3660} {
		query, wantDays := "", days
		if days == 0 {
			wantDays = 365
		} else {
			query = fmt.Sprintf("?days=%d", days)
		}
		result := get(query)
		var got ActivityResponse
		if err := json.Unmarshal(result.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if result.Code != http.StatusOK || len(got.Days) != wantDays || !got.LastSuccessfulSync.Equal(now) || got.Stale || !got.SyncOK {
			t.Fatalf("unexpected response: status %d, %+v", result.Code, got)
		}
	}
	for _, query := range []string{"?days=", "?days=0", "?days=-1", "?days=3661", "?days=abc", "?days=1.5", "?days=999999999999999999999999", "?days=1&days=2"} {
		if got := get(query); got.Code != http.StatusBadRequest {
			t.Errorf("query %q: status %d", query, got.Code)
		}
	}
	baseline := get("?days=3")
	service.failed = true
	failed := get("?days=3")
	var before, after ActivityResponse
	if err := json.Unmarshal(baseline.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(failed.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if failed.Code != http.StatusOK || !after.Stale || after.SyncOK || !after.LastSuccessfulSync.Equal(now) {
		t.Fatalf("failed refresh lost last good history: %+v", after)
	}
	for i := range before.Days {
		if before.Days[i] != after.Days[i] {
			t.Fatal("failed refresh changed activity history")
		}
	}
	service.failed = false
	service.lastSuccess = now.Add(-2 * time.Hour)
	var aged ActivityResponse
	if err := json.Unmarshal(get("?days=1").Body.Bytes(), &aged); err != nil {
		t.Fatal(err)
	}
	if !aged.Stale || !aged.SyncOK {
		t.Fatalf("snapshot age was not reflected: %+v", aged)
	}
}
