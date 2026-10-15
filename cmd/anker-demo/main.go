// anker-demo is a local synthetic Anki sync server used only by the demo setup.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "github.com/nsc99/anker/internal/exporter"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	urlFile := flag.String("url-file", "", "private file in which to write the demo server URL")
	flag.Parse()
	if *urlFile == "" {
		return fmt.Errorf("--url-file is required")
	}
	directory, err := os.MkdirTemp("", "anker-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	collection, err := makeCollection(filepath.Join(directory, "collection.anki2"), time.Now().UTC())
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: demoHandler(collection), ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	endpoint := "http://" + listener.Addr().String()
	if err := os.WriteFile(*urlFile, []byte(endpoint), 0600); err != nil {
		listener.Close()
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		return err
	}
}

func makeCollection(path string, now time.Time) ([]byte, error) {
	db, err := sql.Open("anki-sqlite3", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE col (ver INTEGER, conf TEXT, crt INTEGER);
 INSERT INTO col VALUES (18,'',0);
 CREATE TABLE config (key TEXT, val BLOB);
 INSERT INTO config VALUES ('rollover',X'34');
 CREATE TABLE tags (tag TEXT COLLATE unicase PRIMARY KEY);
 INSERT INTO tags VALUES ('Straße');
 CREATE TABLE revlog (id INTEGER PRIMARY KEY, ease INTEGER, type INTEGER, time INTEGER);`)
	if err != nil {
		return nil, err
	}
	for i := 0; i < 10; i++ {
		ease := 3
		if i < 2 {
			ease = 1
		}
		if _, err := db.Exec("INSERT INTO revlog VALUES (?, ?, 1, 6000)", now.UnixMilli()-int64(i), ease); err != nil {
			return nil, err
		}
	}
	for day := 1; day <= 2; day++ {
		if _, err := db.Exec("INSERT INTO revlog VALUES (?,3,1,6000)", now.AddDate(0, 0, -day).UnixMilli()); err != nil {
			return nil, err
		}
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func demoHandler(collection []byte) http.Handler {
	var failed atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /demo/fail", func(w http.ResponseWriter, r *http.Request) {
		failed.Store(true)
		w.Write([]byte("sync failures enabled\n"))
	})
	mux.HandleFunc("POST /demo/recover", func(w http.ResponseWriter, r *http.Request) {
		failed.Store(false)
		w.Write([]byte("sync failures disabled\n"))
	})
	mux.HandleFunc("POST /sync/{method}", func(w http.ResponseWriter, r *http.Request) {
		if failed.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		decoder, err := zstd.NewReader(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		defer decoder.Close()
		input, err := io.ReadAll(io.LimitReader(decoder, 1<<20))
		if err != nil {
			w.WriteHeader(400)
			return
		}
		var header struct {
			Key string `json:"k"`
		}
		if err := json.Unmarshal([]byte(r.Header.Get("anki-sync")), &header); err != nil {
			w.WriteHeader(400)
			return
		}
		var body []byte
		switch r.PathValue("method") {
		case "hostKey":
			var auth struct {
				User     string `json:"u"`
				Password string `json:"p"`
			}
			if err := json.Unmarshal(input, &auth); err != nil || auth.User != "demo" || auth.Password != "demo" {
				w.WriteHeader(403)
				return
			}
			body = []byte(`{"key":"demo-key"}`)
		case "meta":
			body = []byte(`{"cont":true}`)
		case "download":
			body = collection
		default:
			w.WriteHeader(404)
			return
		}
		if r.PathValue("method") != "hostKey" && header.Key != "demo-key" {
			w.WriteHeader(403)
			return
		}
		encoder, err := zstd.NewWriter(w)
		if err != nil {
			return
		}
		defer encoder.Close()
		encoder.Write(body)
	})
	return mux
}
