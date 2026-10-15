package exporter

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

// Sync protocol 11 uses a JSON anki-sync header and zstd bodies. Only login
// and download are used; the exporter never uploads collection changes.
const syncProtocol = 11
const maxCollectionBytes = 1 << 30

type SyncClient struct {
	config Config
	http   *http.Client
}

func NewSyncClient(c Config) *SyncClient {
	return &SyncClient{c, &http.Client{Timeout: c.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (s *SyncClient) request(ctx context.Context, method, key, session string, input any, output io.Writer, limit int64) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		return err
	}
	compressed := encoder.EncodeAll(payload, nil)
	encoder.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.config.ServerURL+"/sync/"+method, bytes.NewReader(compressed))
	if err != nil {
		return err
	}
	header, err := json.Marshal(map[string]any{"v": syncProtocol, "k": key, "c": "anker", "s": session})
	if err != nil {
		return err
	}
	req.Header.Set("anki-sync", string(header))
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("sync %s request failed", method)
	}
	defer resp.Body.Close()
	// Do not include response bodies or request headers in errors: they may contain secrets.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sync %s: HTTP %d", method, resp.StatusCode)
	}
	decoder, err := zstd.NewReader(resp.Body, zstd.WithDecoderMaxMemory(64<<20), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return fmt.Errorf("sync %s: %w", method, err)
	}
	defer decoder.Close()
	n, err := io.Copy(output, io.LimitReader(decoder, limit+1))
	if err != nil {
		return fmt.Errorf("sync %s body: %w", method, err)
	}
	if n > limit {
		return fmt.Errorf("sync %s exceeds size limit", method)
	}
	return nil
}

// Download returns a private temporary snapshot; the caller validates it before
// replacing the last successful snapshot. Both files reside on the same volume.
func (s *SyncClient) Download(ctx context.Context) (string, error) {
	sessionBytes := make([]byte, 8)
	if _, err := rand.Read(sessionBytes); err != nil {
		return "", err
	}
	session := hex.EncodeToString(sessionBytes)
	var login bytes.Buffer
	if err := s.request(ctx, "hostKey", "", session, map[string]string{"u": s.config.Username, "p": s.config.Password}, &login, 1<<20); err != nil {
		return "", err
	}
	var auth struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(login.Bytes(), &auth); err != nil || auth.Key == "" {
		return "", fmt.Errorf("invalid login response")
	}
	if err := os.MkdirAll(s.config.DataDir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(s.config.DataDir, ".collection-*.anki2")
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(f.Name())
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	if err := s.request(ctx, "download", auth.Key, session, struct{}{}, f, maxCollectionBytes); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}
