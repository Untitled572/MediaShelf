//go:build !windows

package agent

import (
	"context"
	"errors"
	"log"
)

func RunTray(context.Context, Config, *Runner, bool, *log.Logger) error {
	return errors.New("tray mode is supported only on Windows")
}
