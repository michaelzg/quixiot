package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"quixiot/internal/config"
)

func TestLoadFile(t *testing.T) {
	type sample struct {
		Addr  string `yaml:"addr"`
		Count int    `yaml:"count"`
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("addr: :4444\ncount: 7\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var got sample
	if err := config.LoadFile(path, &got); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got.Addr != ":4444" || got.Count != 7 {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestLoadFileEmptyPath(t *testing.T) {
	var got struct{ X string }
	if err := config.LoadFile("", &got); err != nil {
		t.Fatalf("LoadFile empty path: %v", err)
	}
	if got.X != "" {
		t.Fatalf("expected zero value, got %+v", got)
	}
}

func TestLoadFileMissing(t *testing.T) {
	var got struct{ X string }
	if err := config.LoadFile(filepath.Join(t.TempDir(), "nope.yaml"), &got); err == nil {
		t.Fatal("expected error for missing file")
	}
}
