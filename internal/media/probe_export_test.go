package media

import (
	"context"
	"os"
)

func RunCodecProbe(ctx context.Context, b *Broker, in, out *os.File, mode string) error {
	return b.run(ctx, in, out, mode)
}

func CheckCodecIsolation() error {
	_, err := enterRestriction(nil)
	return err
}
