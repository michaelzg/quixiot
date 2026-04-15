package main

import (
	"strings"
	"testing"
)

func TestRunGenCertsRejectsEmptySANs(t *testing.T) {
	err := run([]string{
		"--gen-certs",
		"--cert-dir", t.TempDir(),
		"--sans", "",
	})
	if err == nil {
		t.Fatal("expected error for empty SAN list")
	}
	if !strings.Contains(err.Error(), "at least one SAN") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunGenCertsRejectsNonPositiveValidity(t *testing.T) {
	err := run([]string{
		"--gen-certs",
		"--cert-dir", t.TempDir(),
		"--cert-valid-for=-1h",
	})
	if err == nil {
		t.Fatal("expected error for non-positive validity")
	}
	if !strings.Contains(err.Error(), "validity must be positive") {
		t.Fatalf("unexpected error: %v", err)
	}
}
