package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/digimbyte/photobyte/internal/desktop"
	"github.com/digimbyte/photobyte/internal/media"

	"gioui.org/app"
)

var warpBuild string

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--codec-helper" {
		exitOnError(media.HelperMain(os.Args[2:]))
		return
	}
	data := flag.String("data-dir", "", "local settings and recovery directory")
	safe := flag.Bool("safe-mode", false, "use the Windows software presenter")
	report := flag.String("presentation-report", "", "record the active Windows presentation driver after its first frame")
	quit := flag.Duration("quit-after", 0, "close the desktop after this duration")
	flag.Parse()
	if *safe {
		if warpBuild != "enabled" {
			exitOnError(fmt.Errorf("software presentation requires a build from scripts/build.ps1"))
		}
		exitOnError(os.Setenv("PHOTOBYTE_SOFTWARE", "1"))
	}
	if *report != "" {
		exitOnError(os.Setenv("PHOTOBYTE_PRESENT_REPORT", *report))
	}
	if *data == "" {
		dir, err := os.UserCacheDir()
		exitOnError(err)
		*data = defaultDataDir(dir)
	}
	go func() {
		err := desktop.Run(desktop.Options{DataDir: *data, Open: flag.Arg(0), QuitAfter: time.Duration(*quit), SafeMode: *safe})
		exitOnError(err)
		os.Exit(0)
	}()
	app.Main()
}

func defaultDataDir(root string) string {
	dir := filepath.Join(root, "PhotoByte")
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		return dir
	}
	// Keep the original data path on an upgraded installation so recovery
	// databases and workspace layouts remain in the same location.
	legacy := filepath.Join(root, "DigiPhoto")
	if info, err := os.Stat(legacy); err == nil && info.IsDir() {
		return legacy
	}
	return dir
}

func exitOnError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
