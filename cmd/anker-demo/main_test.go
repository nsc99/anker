package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nsc99/anker/internal/exporter"
)

func TestDemoDownloadAndOutage(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	snapshot, err := makeCollection(filepath.Join(t.TempDir(), "demo.anki2"), now)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(demoHandler(snapshot))
	defer server.Close()
	client := exporter.NewSyncClient(exporter.Config{ServerURL: server.URL, Username: "demo", Password: "demo", DataDir: t.TempDir(), Timeout: time.Second})
	path, err := client.Download(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	collection, err := exporter.ReadCollection(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	stats := collection.Stats(now, time.UTC)
	if stats.ReviewsToday != 10 || stats.StudyStreak != 3 || stats.StudyTimeSeconds != 60 || stats.RetentionPercent == nil || *stats.RetentionPercent != 80 {
		t.Fatalf("unexpected demo data: %+v", stats)
	}
	for _, action := range []string{"fail", "recover"} {
		response, err := http.Post(server.URL+"/demo/"+action, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		_, err = client.Download(context.Background())
		if action == "fail" && (err == nil || !strings.Contains(err.Error(), "503")) {
			t.Fatalf("outage control did not fail sync: %v", err)
		}
		if action == "recover" && err != nil {
			t.Fatalf("recovery control did not restore sync: %v", err)
		}
	}
}
