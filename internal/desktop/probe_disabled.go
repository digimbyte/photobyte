//go:build !windows || !releaseprobe

package desktop

func startReleaseProbe(*window) bool { return false }
