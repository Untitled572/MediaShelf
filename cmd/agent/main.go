package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"mediashelf/internal/agent"
)

func main() {
	configPath := flag.String("config", "agent.json", "path to device-local JSON configuration")
	once := flag.Bool("once", false, "perform one heartbeat/poll cycle, then exit")
	scan := flag.Bool("scan-library", false, "scan configured libraries registered for this device before polling")
	tray := flag.Bool("tray", false, "run the Windows tray agent with status, WebUI, scan, and exit actions")
	flag.Parse()
	logger := log.New(os.Stderr, "mediashelf-agent: ", log.LstdFlags|log.Lmsgprefix)
	c, err := agent.LoadConfig(*configPath)
	if err != nil {
		logger.Fatal(err)
	}
	configAbs, err := filepath.Abs(*configPath)
	if err != nil {
		logger.Fatal(err)
	}
	if c.JournalPath == "" {
		c.JournalPath = filepath.Join(filepath.Dir(configAbs), "agent-journal.json")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	runner := agent.NewRunner(c, logger)
	if *tray {
		if *once {
			logger.Fatal("--tray cannot be combined with --once")
		}
		if err := agent.RunTray(ctx, c, runner, *scan, logger); err != nil && err != context.Canceled {
			logger.Fatal(err)
		}
		return
	}
	if err := runner.Run(ctx, *once, *scan); err != nil && err != context.Canceled {
		logger.Fatal(err)
	}
}
