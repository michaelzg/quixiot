package main

import (
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
	if filepath.Base(source) != "proxy-wifi-good.yaml" {
		t.Fatalf("profile source: got %q", source)
	}
}

func TestResolveProfilePathYAML(t *testing.T) {
	raw := filepath.Join("configs", "custom.yaml")
	if got := resolveProfilePath(raw); got != raw {
		t.Fatalf("resolveProfilePath: want %q got %q", raw, got)
	}
}
