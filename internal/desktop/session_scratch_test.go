package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestScratchCleanupKeepsActiveAndLegacySessions(t *testing.T) {
	root := t.TempDir()
	active, closeActive, err := newSessionScratch(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeActive()
	legacy := filepath.Join(root, "session-10")
	abandoned := filepath.Join(root, "session-11")
	for _, dir := range []string{legacy, abandoned} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{sessionLock, sessionReady} {
		if err := os.WriteFile(filepath.Join(abandoned, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, closeNext, err := newSessionScratch(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeNext()
	if _, err := os.Stat(abandoned); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned scratch was not removed: %v", err)
	}
	for _, dir := range []string{active, legacy} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("retained scratch directory %s is missing: %v", dir, err)
		}
	}
}
