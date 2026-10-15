package exporter

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestStatsRolloverAndRetention(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 30, 3, 0, 0, 0, loc)
	review := func(day, hour, ease, kind int) Review {
		return Review{Timestamp: time.Date(2026, 3, day, hour, 0, 0, 0, loc).UnixMilli(), Ease: ease, Kind: kind, Milliseconds: 1500}
	}
	c := Collection{Rollover: 4, Reviews: []Review{review(27, 5, 3, 1), review(28, 5, 3, 1), review(29, 5, 1, 1), review(30, 2, 3, 1), review(30, 2, 3, 0), review(30, 5, 3, 1)}}
	got := c.Stats(now, loc)
	if got.StudyDay != "2026-03-29" || got.ReviewsToday != 3 || got.StudyStreak != 3 || got.StudyTimeSeconds != 4.5 || got.RetentionPercent == nil || *got.RetentionPercent != 50 {
		t.Fatalf("unexpected stats: %+v", got)
	}
	// Before today's first answer, yesterday's streak is still active.
	got = c.Stats(time.Date(2026, 3, 30, 4, 0, 0, 0, loc), loc)
	if got.StudyStreak != 3 || got.ReviewsToday != 0 || got.RetentionPercent != nil {
		t.Fatalf("unexpected morning stats: %+v", got)
	}
	got = c.Stats(time.Date(2026, 4, 1, 4, 0, 0, 0, loc), loc)
	if got.StudyStreak != 0 {
		t.Fatalf("streak must expire: %+v", got)
	}
}

func fixture(t *testing.T, modern bool) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "collection.anki2")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	schema := `CREATE TABLE col (ver INTEGER, conf TEXT, crt INTEGER); CREATE TABLE revlog (id INTEGER, ease INTEGER, type INTEGER, time INTEGER);`
	if modern {
		schema += `INSERT INTO col VALUES (18, '', 0); CREATE TABLE config (key TEXT, val BLOB); INSERT INTO config VALUES ('rollover', X'36');`
	} else {
		schema += `INSERT INTO col VALUES (11, '{"rollover":6}', 0);`
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO revlog VALUES (?,3,1,1000), (?,0,4,1000)`, time.Now().Add(-time.Hour).UnixMilli(), time.Now().Add(-2*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDownloadSnapshotAndFailureRecovery(t *testing.T) {
	collection := fixture(t, true)
	fail, malformed := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(503)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var header struct {
			V       int
			K, S, C string
		}
		if err := json.Unmarshal([]byte(r.Header.Get("anki-sync")), &header); err != nil {
			t.Error(err)
		}
		if header.V != syncProtocol || header.S == "" {
			t.Errorf("bad sync header: %+v", header)
		}
		dec, err := zstd.NewReader(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		defer dec.Close()
		payload, err := io.ReadAll(dec)
		if err != nil {
			t.Error(err)
			return
		}
		var response []byte
		switch r.URL.Path {
		case "/sync/hostKey":
			var login map[string]string
			if err := json.Unmarshal(payload, &login); err != nil {
				t.Error(err)
			}
			if login["u"] != "user" || login["p"] != "pass" || header.K != "" {
				t.Error("incorrect authentication")
			}
			response = []byte(`{"key":"secret-key"}`)
		case "/sync/download":
			if header.K != "secret-key" || string(payload) != "{}" {
				t.Error("invalid download request")
			}
			response = collection
			if malformed {
				response = []byte("not a collection")
			}
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		enc, err := zstd.NewWriter(w)
		if err != nil {
			t.Error(err)
			return
		}
		enc.Write(response)
		enc.Close()
	}))
	defer server.Close()
	config := Config{ServerURL: server.URL, Username: "user", Password: "pass", DataDir: t.TempDir(), Location: time.UTC, Timeout: time.Second, StaleAfter: time.Hour}
	s := NewService(config)
	req := httptest.NewRequest("GET", "/stats", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("expected unavailable before initial sync: %d", w.Code)
	}
	s.refresh(context.Background())
	if s.failed || s.collection == nil || s.collection.Rollover != 6 || len(s.collection.Reviews) != 1 {
		t.Fatalf("sync failed: %+v", s)
	}
	original := s.lastSuccess
	for _, bad := range []string{"invalid database", "HTTP error"} {
		malformed = bad == "invalid database"
		fail = bad == "HTTP error"
		s.refresh(context.Background())
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		var got Response
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || !got.Stale || got.SyncOK || !got.LastSuccessfulSync.Equal(original) {
			t.Fatalf("last good data was lost: %s", w.Body.String())
		}
		col, err := ReadCollection(context.Background(), s.snapshotPath())
		if err != nil || col.Rollover != 6 {
			t.Fatalf("last good snapshot damaged: %v", err)
		}
	}
	files, err := filepath.Glob(filepath.Join(config.DataDir, ".collection-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary files leaked: %v, %v", files, err)
	}
	// A restart can serve the last good collection even while the server is down.
	restarted := NewService(config)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	restarted.loadSnapshot(context.Background())
	restarted.refresh(ctx)
	if restarted.collection == nil || !restarted.lastSuccess.Equal(original) {
		t.Fatal("snapshot not restored after restart")
	}
}

func TestLegacyCollection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.anki2")
	if err := os.WriteFile(path, fixture(t, false), 0600); err != nil {
		t.Fatal(err)
	}
	col, err := ReadCollection(context.Background(), path)
	if err != nil || col.Rollover != 6 {
		t.Fatalf("legacy rollover: %+v, %v", col, err)
	}
}
