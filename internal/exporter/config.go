package exporter

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Listen, ServerURL, Username, Password, DataDir string
	DatabaseURL                                    string
	Interval, Timeout, StaleAfter                  time.Duration
	Location                                       *time.Location
}

func LoadConfig() (Config, error) {
	c := Config{Listen: env("ANKER_LISTEN", "127.0.0.1:8080"), ServerURL: os.Getenv("ANKER_SYNC_URL"), Username: os.Getenv("ANKER_USERNAME"), DataDir: env("ANKER_DATA_DIR", "data")}
	c.DatabaseURL = os.Getenv("ANKER_DATABASE_URL")
	if file := os.Getenv("ANKER_DATABASE_URL_FILE"); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return c, fmt.Errorf("read database URL file: %w", err)
		}
		c.DatabaseURL = strings.TrimSpace(string(b))
		if c.DatabaseURL == "" {
			return c, fmt.Errorf("database URL file is empty")
		}
	}
	c.Password = os.Getenv("ANKER_PASSWORD")
	if file := os.Getenv("ANKER_PASSWORD_FILE"); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return c, fmt.Errorf("read password file: %w", err)
		}
		c.Password = strings.TrimRight(string(b), "\r\n")
	}
	u, err := url.Parse(c.ServerURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return c, fmt.Errorf("ANKER_SYNC_URL must be an HTTP(S) server base URL without credentials, query, or fragment")
	}
	c.ServerURL = strings.TrimRight(c.ServerURL, "/")
	if c.Username == "" || c.Password == "" {
		return c, fmt.Errorf("ANKER_USERNAME and ANKER_PASSWORD (or ANKER_PASSWORD_FILE) are required")
	}
	c.Location, err = time.LoadLocation(env("ANKER_TIMEZONE", "UTC"))
	if err != nil {
		return c, fmt.Errorf("ANKER_TIMEZONE: %w", err)
	}
	for _, d := range []struct {
		key, fallback string
		dest          *time.Duration
	}{
		{"ANKER_SYNC_INTERVAL", "15m", &c.Interval}, {"ANKER_SYNC_TIMEOUT", "5m", &c.Timeout}, {"ANKER_STALE_AFTER", "30m", &c.StaleAfter},
	} {
		*d.dest, err = time.ParseDuration(env(d.key, d.fallback))
		if err != nil || *d.dest <= 0 {
			return c, fmt.Errorf("%s must be a positive duration", d.key)
		}
	}
	return c, nil
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
