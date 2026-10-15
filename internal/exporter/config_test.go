package exporter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseURLFilePrecedence(t *testing.T) {
	t.Setenv("ANKER_SYNC_URL", "https://sync.example")
	t.Setenv("ANKER_USERNAME", "test")
	t.Setenv("ANKER_PASSWORD", "test")
	t.Setenv("ANKER_PASSWORD_FILE", "")
	t.Setenv("ANKER_DATABASE_URL", "postgresql://unused/db")
	file := filepath.Join(t.TempDir(), "database-url")
	t.Setenv("ANKER_DATABASE_URL_FILE", file)
	expected := "postgresql://test:secret@localhost/anker"
	if err := os.WriteFile(file, []byte(expected+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.DatabaseURL != expected {
		t.Fatal("mounted database secret did not take precedence")
	}
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(); err == nil {
		t.Fatal("empty database secret must not silently enable file mode")
	}
}
