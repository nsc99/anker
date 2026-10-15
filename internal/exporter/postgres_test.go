package exporter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// ANKER_TEST_DATABASE_URL must point to a disposable database: this test drops
// the exporter's schema. It never uses ANKER_DATABASE_URL or real Anki credentials.
func TestPostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("ANKER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ANKER_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := NewPostgresStore(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	defer store.pool.Exec(context.Background(), "DROP SCHEMA anker CASCADE")
	if _, err := store.pool.Exec(ctx, "TRUNCATE anker.reviews, anker.collection_state"); err != nil {
		t.Fatal(err)
	}
	col, _, err := store.Load(ctx)
	if err != nil || col != nil {
		t.Fatalf("new database must have no snapshot: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	first := &Collection{Rollover: 4, Reviews: []Review{{Timestamp: now.Add(-time.Hour).UnixMilli(), Ease: 3, Kind: 1, Milliseconds: 1000}}}
	fetch := func() (*Collection, time.Time, error) { return first, now, nil }
	if _, _, err := store.Sync(ctx, 0, fetch); err != nil {
		t.Fatal(err)
	}
	check := func(expected *Collection, stamp time.Time) {
		t.Helper()
		got, last, err := store.Load(ctx)
		if err != nil || got == nil || got.Rollover != expected.Rollover || len(got.Reviews) != len(expected.Reviews) || !last.Equal(stamp) {
			t.Fatalf("snapshot or metadata mismatch: %+v, %s, %v", got, last, err)
		}
		for i := range got.Reviews {
			if got.Reviews[i] != expected.Reviews[i] {
				t.Fatalf("review mismatch: %+v", got.Reviews[i])
			}
		}
	}
	check(first, now)
	downloaded := false
	if _, _, err := store.Sync(ctx, time.Hour, func() (*Collection, time.Time, error) { downloaded = true; return first, now, nil }); err != nil || downloaded {
		t.Fatalf("recent shared snapshot did not suppress a duplicate sync: %v", err)
	}
	// A failure after DELETE but during COPY must roll back the entire refresh.
	duplicate := &Collection{Rollover: 6, Reviews: []Review{first.Reviews[0], first.Reviews[0]}}
	if _, _, err := store.Sync(ctx, 0, func() (*Collection, time.Time, error) { return duplicate, now.Add(time.Minute), nil }); err == nil {
		t.Fatal("duplicate review IDs should fail")
	}
	check(first, now)
	if _, _, err := store.Sync(ctx, 0, func() (*Collection, time.Time, error) { return nil, time.Time{}, errors.New("download failed") }); err == nil {
		t.Fatal("download failure should propagate")
	}
	check(first, now)
	// A second replica reads committed data while another replica refreshes.
	replica, err := NewPostgresStore(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	second := &Collection{Rollover: 6, Reviews: []Review{{Timestamp: now.UnixMilli(), Ease: 1, Kind: 1, Milliseconds: 2000}}}
	done := make(chan error, 1)
	go func() {
		_, _, err := store.Sync(ctx, 0, func() (*Collection, time.Time, error) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, time.Time{}, ctx.Err()
			}
			return second, now.Add(time.Minute), nil
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("refresh did not acquire its lock")
	}
	called := false
	restored, last, err := replica.Sync(ctx, 0, func() (*Collection, time.Time, error) {
		called = true
		return nil, time.Time{}, errors.New("must not download twice")
	})
	close(release)
	if refreshErr := <-done; refreshErr != nil {
		t.Fatal(refreshErr)
	}
	if err != nil || called || restored == nil || restored.Reviews[0] != first.Reviews[0] || !last.Equal(now) {
		t.Fatalf("replica did not retain committed snapshot: %v", err)
	}
	check(second, now.Add(time.Minute))

	t.Run("service restart and temporary files", func(t *testing.T) {
		snapshot := fixture(t, true)
		failed := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if failed {
				w.WriteHeader(503)
				return
			}
			var body []byte
			switch r.URL.Path {
			case "/sync/hostKey":
				body = []byte(`{"key":"test-key"}`)
			case "/sync/meta":
				body = []byte(`{"cont":true}`)
			case "/sync/download":
				body = snapshot
			default:
				w.WriteHeader(404)
				return
			}
			encoder, err := zstd.NewWriter(w)
			if err != nil {
				t.Error(err)
				return
			}
			defer encoder.Close()
			encoder.Write(body)
		}))
		defer server.Close()
		// This path intentionally cannot be used; PostgreSQL mode must ignore it.
		config := Config{DatabaseURL: databaseURL, ServerURL: server.URL, Username: "test", Password: "test", DataDir: filepath.Join(t.TempDir(), "must-not-exist"), Location: time.UTC, Timeout: 5 * time.Second, StaleAfter: time.Hour}
		service := NewService(config)
		if err := service.Prepare(ctx); err != nil {
			t.Fatal(err)
		}
		temporary := service.tempDir
		service.refresh(ctx)
		if service.failed || service.collection == nil || service.collection.Rollover != 6 {
			service.Close()
			t.Fatal("PostgreSQL refresh failed")
		}
		stamp := service.lastSuccess
		entries, err := os.ReadDir(temporary)
		if err != nil || len(entries) != 0 {
			t.Fatalf("temporary snapshot leaked: %v", err)
		}
		if _, err := os.Stat(config.DataDir); !os.IsNotExist(err) {
			t.Fatal("PostgreSQL mode created a persistent SQLite directory")
		}
		service.Close()
		if _, err := os.Stat(temporary); !os.IsNotExist(err) {
			t.Fatal("temporary directory survived shutdown")
		}
		restarted := NewService(config)
		if err := restarted.Prepare(ctx); err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()
		restarted.loadSnapshot(ctx)
		if restarted.collection == nil || !restarted.lastSuccess.Equal(stamp) {
			t.Fatal("database snapshot not restored after restart")
		}
		failed = true
		restarted.refresh(ctx)
		result := httptest.NewRecorder()
		restarted.Handler().ServeHTTP(result, httptest.NewRequest("GET", "/stats", nil))
		body, _ := io.ReadAll(result.Result().Body)
		if result.Code != 200 || !strings.Contains(string(body), `"stale":true`) || !restarted.lastSuccess.Equal(stamp) {
			t.Fatalf("failed refresh lost last good data: %s", body)
		}
	})
}

func TestPostgresErrorsDoNotExposeCredentials(t *testing.T) {
	err := postgresError("connect", errors.New("postgres://user:secret@host/db"))
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "user") {
		t.Fatal("database error exposed credentials")
	}
}
