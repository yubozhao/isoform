package daemon

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"yansu-agent/internal/config"
)

// Run waits for shutdown locally. Older service definitions always restart an exited daemon.
func Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := config.WritePIDFile(); err != nil {
		return err
	}
	defer config.RemovePIDFile()
	<-ctx.Done()
	return nil
}
