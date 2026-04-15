package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfilePassthrough(t *testing.T) {
	profile, source, err := loadProfile("passthrough")
	if err != nil {
		t.Fatalf("loadProfile: %v", err)
	}
	if profile.Name != "passthrough" {
		t.Fatalf("profile name: want passthrough got %q", profile.Name)
	}
	if source != "builtin:passthrough" {
		t.Fatalf("profile source: want builtin:passthrough got %q", source)
	}
}

func TestLoadProfileNamed(t *testing.T) {
	profile, source, err := loadProfile("wifi-good")
	if err != nil {
		t.Fatalf("loadProfile: %v", err)
	}
	if profile.Name != "wifi-good" {
		t.Fatalf("profile name: want wifi-good got %q", profile.Name)
	}
	if source != "builtin:wifi-good" {
		t.Fatalf("profile source: got %q", source)
	}
}

func TestLoadProfileNamedFromUnrelatedWorkingDirectory(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Chdir(t.TempDir())

	profile, source, err := loadProfile("flaky")
	if err != nil {
		t.Fatalf("loadProfile from temp dir: %v", err)
	}
	if profile.Name != "flaky" || source != "builtin:flaky" {
		t.Fatalf("loadProfile from temp dir: got name=%q source=%q", profile.Name, source)
	}

	t.Chdir(wd)
}

func TestResolveProfilePathYAML(t *testing.T) {
	raw := filepath.Join("configs", "custom.yaml")
	if got := resolveProfilePath(raw); got != raw {
		t.Fatalf("resolveProfilePath: want %q got %q", raw, got)
	}
}
