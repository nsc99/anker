package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type Service struct {
	config      Config
	client      *SyncClient
	mu          sync.RWMutex
	collection  *Collection
	lastSuccess time.Time
	failed      bool
	database    *PostgresStore
	tempDir     string
}

type Response struct {
	Stats
	LastSuccessfulSync time.Time `json:"last_successful_sync"`
	Stale              bool      `json:"stale"`
	SyncOK             bool      `json:"sync_ok"`
}

func NewService(c Config) *Service { return &Service{config: c, client: NewSyncClient(c)} }

// Prepare initializes durable storage before the HTTP server starts.
func (s *Service) Prepare(ctx context.Context) error {
	if s.config.DatabaseURL == "" {
		return nil
	}
	database, err := NewPostgresStore(ctx, s.config.DatabaseURL)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "anker-sync-")
	if err != nil {
		database.Close()
		return err
	}
	s.database = database
	s.tempDir = directory
	s.client.config.DataDir = directory
	return nil
}

func (s *Service) Close() {
	if s.database != nil {
		s.database.Close()
	}
	if s.tempDir != "" {
		os.RemoveAll(s.tempDir)
	}
}

func (s *Service) snapshotPath() string { return filepath.Join(s.config.DataDir, "collection.anki2") }

func (s *Service) loadSnapshot(ctx context.Context) {
	if s.database != nil {
		col, syncedAt, err := s.database.Load(ctx)
		if err != nil {
			slog.Error("restore PostgreSQL snapshot failed", "error", err)
			return
		}
		s.mu.Lock()
		s.collection = col
		s.lastSuccess = syncedAt
		s.mu.Unlock()
		return
	}

	if col, err := ReadCollection(ctx, s.snapshotPath()); err == nil {
		if info, err := os.Stat(s.snapshotPath()); err == nil {
			s.mu.Lock()
			s.collection = col
			s.lastSuccess = info.ModTime()
			s.mu.Unlock()
		}
	}
}

func (s *Service) Run(ctx context.Context) {
	s.loadSnapshot(ctx)
	s.refresh(ctx)
	timer := time.NewTicker(s.config.Interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.refresh(ctx)
		}
	}
}

func (s *Service) refresh(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, s.config.Timeout)
	defer cancel()
	err := s.sync(ctx)
	s.mu.Lock()
	s.failed = err != nil
	s.mu.Unlock()
	if err != nil {
		slog.Error("collection refresh failed", "error", err)
	} else {
		slog.Info("collection refreshed")
	}
}
func (s *Service) sync(ctx context.Context) error {
	if s.config.DatabaseURL != "" && s.database == nil {
		return fmt.Errorf("PostgreSQL storage was not initialized")
	}
	if s.database != nil {
		col, syncedAt, err := s.database.Sync(ctx, s.config.Interval, func() (*Collection, time.Time, error) {
			path, err := s.client.Download(ctx)
			if err != nil {
				return nil, time.Time{}, err
			}
			defer os.Remove(path)
			col, err := ReadCollection(ctx, path)
			if err != nil {
				return nil, time.Time{}, err
			}
			return col, time.Now().UTC(), nil
		})
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.collection = col
		s.lastSuccess = syncedAt
		s.failed = false
		s.mu.Unlock()
		return nil
	}

	path, err := s.client.Download(ctx)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	col, err := ReadCollection(ctx, path)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := os.Chtimes(path, now, now); err != nil {
		return err
	}
	if err := os.Rename(path, s.snapshotPath()); err != nil {
		return err
	}
	s.mu.Lock()
	s.collection = col
	s.lastSuccess = now
	s.failed = false
	s.mu.Unlock()
	return nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		s.mu.RLock()
		col, last, failed := s.collection, s.lastSuccess, s.failed
		s.mu.RUnlock()
		if col == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "no successful sync yet"})
			return
		}
		now := time.Now()
		json.NewEncoder(w).Encode(Response{Stats: col.Stats(now, s.config.Location), LastSuccessfulSync: last, Stale: failed || now.Sub(last) > s.config.StaleAfter, SyncOK: !failed})
	})
	mux.HandleFunc("GET /activity", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		count := 365
		if values, present := r.URL.Query()["days"]; present {
			var err error
			count, err = strconv.Atoi(values[0])
			if err != nil || len(values) != 1 || count < 1 || count > 3660 {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "days must be an integer between 1 and 3660"})
				return
			}
		}
		s.mu.RLock()
		col, last, failed := s.collection, s.lastSuccess, s.failed
		s.mu.RUnlock()
		if col == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "no successful sync yet"})
			return
		}
		now := time.Now()
		json.NewEncoder(w).Encode(ActivityResponse{Activity: col.Activity(now, s.config.Location, count), LastSuccessfulSync: last, Stale: failed || now.Sub(last) > s.config.StaleAfter, SyncOK: !failed})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		ready := s.collection != nil && !s.failed && time.Since(s.lastSuccess) <= s.config.StaleAfter
		s.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(map[string]bool{"ready": ready})
	})
	return mux
}
