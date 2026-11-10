package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	sessionLock  = ".session.lock"
	sessionReady = ".photobyte-session"
)

// A ready marker is written only after the session owns its lock. Existing
// sessions without a marker may belong to an older running editor.
func newSessionScratch(dataDir string) (string, func(), error) {
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return "", nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(root, "session-*")
	if err != nil {
		return "", nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, sessionLock), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		removeSessionScratch(root, dir)
		return "", nil, err
	}
	cleanup := func() {
		file.Close()
		removeSessionScratch(root, dir)
	}
	if locked, err := tryScratchLock(file); err != nil || !locked {
		cleanup()
		return "", nil, errors.Join(errors.New("cannot lock the editor scratch directory"), err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionReady), nil, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	removeAbandonedSessions(root, dir)
	return dir, cleanup, nil
}

func removeAbandonedSessions(root, active string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !isSessionName(entry.Name()) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if dir == active {
			continue
		}
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || real != dir {
			continue
		}
		if info, err := os.Lstat(filepath.Join(dir, sessionReady)); err != nil || !info.Mode().IsRegular() {
			continue
		}
		file, err := os.OpenFile(filepath.Join(dir, sessionLock), os.O_RDWR, 0)
		if err != nil {
			continue
		}
		locked, err := tryScratchLock(file)
		file.Close()
		if err == nil && locked {
			removeSessionScratch(root, dir)
		}
	}
}

func isSessionName(name string) bool {
	suffix, ok := strings.CutPrefix(name, "session-")
	_, err := strconv.ParseUint(suffix, 10, 64)
	return ok && err == nil
}

func removeSessionScratch(root, dir string) {
	real, err := filepath.EvalSymlinks(dir)
	if err == nil && real == dir && filepath.Dir(real) == root && isSessionName(filepath.Base(real)) {
		_ = os.RemoveAll(real)
	}
}
