package exporter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestSyncRedirectPreservesPost(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/octet-stream" {
					t.Errorf("invalid sync method or content type")
				}
				var header map[string]any
				if err := json.Unmarshal([]byte(r.Header.Get("anki-sync")), &header); err != nil || header["k"] != "host-key" || header["c"] != syncClientVersion {
					t.Error("missing authentication header")
				}
				dec, err := zstd.NewReader(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				defer dec.Close()
				payload, err := io.ReadAll(dec)
				if err != nil || string(payload) != `{"u":"alice"}` {
					t.Errorf("POST body was not preserved: %q, %v", payload, err)
				}
				if r.URL.Path == "/sync/hostKey" {
					w.Header().Set("Location", "/proxy/sync/hostKey")
					w.WriteHeader(status)
					return
				}
				enc, err := zstd.NewWriter(w)
				if err != nil {
					t.Error(err)
					return
				}
				defer enc.Close()
				enc.Write([]byte(`{"key":"ok"}`))
			}))
			defer server.Close()
			client := NewSyncClient(Config{ServerURL: server.URL, Timeout: time.Second})
			var result bytes.Buffer
			err := client.request(context.Background(), "hostKey", "host-key", "session", map[string]string{"u": "alice"}, &result, 1024)
			if err != nil || result.String() != `{"key":"ok"}` || requests.Load() != 2 {
				t.Fatalf("redirect failed: %v, requests: %d", err, requests.Load())
			}
		})
	}
}

func TestSyncRedirectRejectsCredentialLeaks(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer other.Close()
	for _, location := range []string{other.URL + "/sync/hostKey", "/login", "/sync/hostKey?token=secret-token"} {
		t.Run(location, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusSeeOther)
				w.Write([]byte("secret-response"))
			}))
			defer server.Close()
			client := NewSyncClient(Config{ServerURL: server.URL, Timeout: time.Second})
			err := client.request(context.Background(), "hostKey", "secret-key", "session", map[string]string{"p": "secret-password"}, io.Discard, 1024)
			if err == nil || !strings.Contains(err.Error(), "redirect refused") {
				t.Fatalf("expected redirect refusal: %v", err)
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatal("error exposed a secret")
			}
			if requests.Load() != 1 || leaked.Load() != 0 {
				t.Fatal("redirect received credentials")
			}
		})
	}
}

func TestSyncRedirectLoop(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", "/sync/hostKey")
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer server.Close()
	client := NewSyncClient(Config{ServerURL: server.URL, Timeout: time.Second})
	err := client.request(context.Background(), "hostKey", "", "", struct{}{}, io.Discard, 1024)
	if err == nil || !strings.Contains(err.Error(), "redirect limit exceeded") || requests.Load() != 6 {
		t.Fatalf("loop was not bounded: %v, requests: %d", err, requests.Load())
	}
}

func TestSafeSyncRedirect(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		allowed  bool
	}{
		{"http://anki.example/sync/hostKey", "https://anki.example/sync/hostKey", true},
		{"http://anki.example:80/sync/hostKey", "https://anki.example:443/sync/hostKey", true},
		{"https://anki.example/sync/hostKey", "http://anki.example/sync/hostKey", false},
		{"https://anki.example/sync/hostKey", "https://other.example/sync/hostKey", false},
		{"https://anki.example/sync/hostKey", "https://anki.example:8443/sync/hostKey", false},
		{"https://anki.example/sync/hostKey", "https://anki.example/login", false},
		{"https://anki.example/sync/hostKey", "https://user:pass@anki.example/sync/hostKey", false},
		{"https://anki.example/sync/hostKey", "https://anki.example/sync/download", false},
	} {
		from, _ := url.Parse(tc.from)
		to, _ := url.Parse(tc.to)
		if got := safeSyncRedirect(from, to, "hostKey"); got != tc.allowed {
			t.Errorf("redirect %s to %s: got %v", tc.from, tc.to, got)
		}
	}
}

func TestAnkiWebMetadataRouting(t *testing.T) {
	for _, tc := range []struct {
		target, method string
		status         int
		allowed        bool
	}{
		{"https://sync2.ankiweb.net/", "meta", 308, true},
		{"https://sync12.ankiweb.net", "meta", 308, true},
		{"https://sync2.ankiweb.net/", "hostKey", 308, false},
		{"https://sync2.ankiweb.net/", "meta", 303, false},
		{"https://evil.ankiweb.net/", "meta", 308, false},
		{"https://sync2.ankiweb.net.evil.example/", "meta", 308, false},
		{"http://sync2.ankiweb.net/", "meta", 308, false},
		{"https://sync2.ankiweb.net/?token=secret", "meta", 308, false},
		{"https://sync2.ankiweb.net/login", "meta", 308, false},
	} {
		from, _ := url.Parse("https://sync.ankiweb.net/sync/" + tc.method)
		to, _ := url.Parse(tc.target)
		target, allowed := resolveSyncRedirect(from, to, tc.method, tc.status)
		if allowed != tc.allowed {
			t.Errorf("unexpected routing allowance for %s: %v", tc.target, allowed)
		}
		if allowed && target.Path != "/sync/meta" {
			t.Error("metadata was not routed to the sync endpoint")
		}
	}
}

func TestMetadataDiscoveryRoutesDownload(t *testing.T) {
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload []byte
		switch r.URL.Path {
		case "/sync/hostKey":
			payload = []byte(`{"key":"key"}`)
		case "/sync/meta":
			w.Header().Set("Location", "/account/sync/meta")
			w.WriteHeader(307)
			return
		case "/account/sync/meta":
			payload = []byte(`{"cont":true}`)
		case "/account/sync/download":
			downloads.Add(1)
			payload = []byte("snapshot")
		default:
			t.Errorf("incorrect endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		enc, err := zstd.NewWriter(w)
		if err != nil {
			t.Error(err)
			return
		}
		defer enc.Close()
		enc.Write(payload)
	}))
	defer server.Close()
	client := NewSyncClient(Config{ServerURL: server.URL, Timeout: time.Second, Username: "user", Password: "pass", DataDir: t.TempDir()})
	if _, err := client.Download(context.Background()); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 1 {
		t.Fatal("download did not use discovered server")
	}
	if client.config.ServerURL != server.URL {
		t.Fatal("shared client configuration was mutated")
	}
}

func TestSync303WithoutLocation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(303); w.Write([]byte("secret-response")) }))
	defer server.Close()
	client := NewSyncClient(Config{ServerURL: server.URL, Timeout: time.Second})
	err := client.request(context.Background(), "download", "secret-key", "session", struct{}{}, io.Discard, 1024)
	if err == nil || !strings.Contains(err.Error(), "without a valid Location") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("missing or unsafe diagnostic: %v", err)
	}
}
