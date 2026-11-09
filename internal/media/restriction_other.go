//go:build !windows

package media

import (
	"context"
	"os"
	"os/exec"
)

type codecSandbox struct {
	executable, scratch string
}

func newCodecSandbox(executable, scratch string) codecSandbox {
	return codecSandbox{executable: executable, scratch: scratch}
}

func (s codecSandbox) run(ctx context.Context, stdio [3]*os.File, args []string) error {
	cmd := exec.CommandContext(ctx, s.executable, append([]string{"--codec-helper"}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdio[0], stdio[1], stdio[2]
	cmd.Dir = s.scratch
	cmd.Env = []string{"GOMEMLIMIT=448MiB", "GOMAXPROCS=1"}
	if err := cmd.Start(); err != nil {
		return err
	}
	stdio[1].Close()
	stdio[2].Close()
	return cmd.Wait()
}

func enterRestriction(args []string) ([]string, error) { return args, nil }
