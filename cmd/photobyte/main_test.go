package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDataDirReusesExistingWorkspace(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "PhotoByte")
	legacy := filepath.Join(root, "DigiPhoto")
	if got := defaultDataDir(root); got != current {
		t.Fatalf("fresh path %q, want %q", got, current)
	}
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := defaultDataDir(root); got != legacy {
		t.Fatalf("upgraded path %q, want %q", got, legacy)
	}
	if err := os.Mkdir(current, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := defaultDataDir(root); got != current {
		t.Fatalf("existing PhotoByte path %q, want %q", got, current)
	}
}
