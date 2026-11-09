package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"digiphoto/internal/desktop"
	"digiphoto/internal/media"

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
		exitOnError(os.Setenv("DIGIPHOTO_SOFTWARE", "1"))
	}
	if *report != "" {
		exitOnError(os.Setenv("DIGIPHOTO_PRESENT_REPORT", *report))
	}
	if *data == "" {
		dir, err := os.UserCacheDir()
		exitOnError(err)
		*data = filepath.Join(dir, "DigiPhoto")
	}
	go func() {
		err := desktop.Run(desktop.Options{DataDir: *data, Open: flag.Arg(0), QuitAfter: time.Duration(*quit), SafeMode: *safe})
		exitOnError(err)
		os.Exit(0)
	}()
	app.Main()
}

func exitOnError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
