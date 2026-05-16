package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingFileCapsActiveAndBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := NewRotatingFile(path, 24, 3)
	if err != nil {
		t.Fatalf("NewRotatingFile: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := fmt.Fprintf(w, "line-%02d-abcdefgh\n", i); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, p := range []string{path, path + ".1", path + ".2"} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if info.Size() > 24 {
			t.Fatalf("%s size: want <=24 got %d", p, info.Size())
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("oldest backup should not exist: %v", err)
	}
}

func TestRotatingFileTruncatesOversizedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := NewRotatingFile(path, 12, 2)
	if err != nil {
		t.Fatalf("NewRotatingFile: %v", err)
	}
	if n, err := w.Write([]byte("this record is much larger than the file cap\n")); err != nil || n == 0 {
		t.Fatalf("Write: n=%d err=%v", n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat active log: %v", err)
	}
	if info.Size() > 12 {
		t.Fatalf("active log size: want <=12 got %d", info.Size())
	}
}
