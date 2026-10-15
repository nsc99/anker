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
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Sync protocol 11 uses a JSON anki-sync header and zstd bodies. Only login
// and download are used; the exporter never uploads collection changes.
const syncProtocol = 11
const maxCollectionBytes = 1 << 30

// Anki-format compatibility identifier, verified from upstream .version:
// https://github.com/ankitects/anki/blob/main/.version (2026-10-15).
// Keep the exporter build identity visible in both protocol version strings.
const ankiCompatibilityVersion = "26.09.3"
const syncClientVersion = ankiCompatibilityVersion + ",anker," + runtime.GOOS
const syncMetaClientVersion = "anki," + ankiCompatibilityVersion + " (anker)," + runtime.GOOS

type SyncClient struct {
	config Config
	http   *http.Client
}

func NewSyncClient(c Config) *SyncClient {
	return &SyncClient{c, &http.Client{Timeout: c.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (s *SyncClient) request(ctx context.Context, method, key, session string, input any, output io.Writer, limit int64) error {
	return s.requestAt(ctx, method, key, session, input, output, limit, nil)
}

func (s *SyncClient) requestAt(ctx context.Context, method, key, session string, input any, output io.Writer, limit int64, resolvedBase *string) error {
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
	header, err := json.Marshal(map[string]any{"v": syncProtocol, "k": key, "c": syncClientVersion, "s": session})
	if err != nil {
		return err
	}
	endpoint, err := url.Parse(s.config.ServerURL + "/sync/" + method)
	if err != nil {
		return fmt.Errorf("invalid sync endpoint")
	}
	var resp *http.Response
	for redirects := 0; ; redirects++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(compressed))
		if err != nil {
			return fmt.Errorf("invalid sync request")
		}
		req.Header.Set("anki-sync", string(header))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("User-Agent", "anker")
		resp, err = s.http.Do(req)
		// Request errors can contain redirect URLs or tokens; do not log them.
		if err != nil {
			return fmt.Errorf("sync %s request failed (check connectivity, TLS, and timeout)", method)
		}
		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			target, err := resp.Location()
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("sync %s: HTTP %d without a valid Location header; check server routing and logs", method, resp.StatusCode)
			}
			if redirects >= 5 {
				return fmt.Errorf("sync %s: redirect limit exceeded", method)
			}
			target, allowed := resolveSyncRedirect(endpoint, target, method, resp.StatusCode)
			if !allowed {
				return fmt.Errorf("sync %s: HTTP %d redirect refused; use the direct Anki server base URL in ANKER_SYNC_URL and check proxy authentication", method, resp.StatusCode)
			}
			endpoint = target
			continue
		}
		break
	}
	defer resp.Body.Close()
	// Do not log response bodies, URLs, or headers: they may contain secrets.
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
	if resolvedBase != nil {
		*resolvedBase = strings.TrimSuffix(endpoint.String(), "/sync/"+method)
	}
	return nil
}

// Replay a sync POST only within the configured origin, or for a default-port
// HTTP-to-HTTPS upgrade on the same hostname. Login pages and unrelated origins must
// never receive credentials or host keys, even if a proxy redirects there.
func safeSyncRedirect(from, to *url.URL, method string) bool {
	if to.User != nil || to.RawQuery != "" || to.Fragment != "" || to.Opaque != "" {
		return false
	}
	sameOrigin := strings.EqualFold(from.Scheme, to.Scheme) && strings.EqualFold(from.Host, to.Host)
	upgrade := from.Scheme == "http" && to.Scheme == "https" && strings.EqualFold(from.Hostname(), to.Hostname()) &&
		(from.Port() == "" || from.Port() == "80") && (to.Port() == "" || to.Port() == "443")
	return (sameOrigin || upgrade) && strings.HasSuffix(to.Path, "/sync/"+method)
}

// AnkiWeb's metadata endpoint returns a 308 with a server base URL. Resolve
// only official HTTPS sync hosts, and never send username/password in a handoff.
func resolveSyncRedirect(from, to *url.URL, method string, status int) (*url.URL, bool) {
	if safeSyncRedirect(from, to, method) {
		return to, true
	}
	if method != "meta" || status != http.StatusPermanentRedirect || !ankiWebSyncURL(from) || !ankiWebSyncURL(to) {
		return nil, false
	}
	if to.Path != "" && to.Path != "/" {
		return nil, false
	}
	resolved := *to
	resolved.Path = "/sync/meta"
	return &resolved, true
}

func ankiWebSyncURL(u *url.URL) bool {
	if u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".ankiweb.net") {
		return false
	}
	name := strings.TrimSuffix(host, ".ankiweb.net")
	if !strings.HasPrefix(name, "sync") {
		return false
	}
	for _, digit := range strings.TrimPrefix(name, "sync") {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
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
	// Metadata discovery must happen before a full download, especially on
	// AnkiWeb where an account may live on another sync server.
	client := *s
	var metadata bytes.Buffer
	var resolvedBase string
	if err := client.requestAt(ctx, "meta", auth.Key, session, map[string]any{"v": syncProtocol, "cv": syncMetaClientVersion}, &metadata, 1<<20, &resolvedBase); err != nil {
		return "", err
	}
	var meta struct {
		Continue bool `json:"cont"`
	}
	if err := json.Unmarshal(metadata.Bytes(), &meta); err != nil {
		return "", fmt.Errorf("invalid sync metadata response")
	}
	if !meta.Continue {
		return "", fmt.Errorf("server refused sync; check server compatibility and account status")
	}
	client.config.ServerURL = resolvedBase
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
	if err := client.request(ctx, "download", auth.Key, session, struct{}{}, f, maxCollectionBytes); err != nil {
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
