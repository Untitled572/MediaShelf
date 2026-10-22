package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"mediashelf/internal/agent"
)

func main() {
	configPath := flag.String("config", "agent.json", "path to device-local JSON configuration")
	once := flag.Bool("once", false, "perform one heartbeat/poll cycle, then exit")
	scan := flag.Bool("scan-library", false, "scan configured libraries registered for this device before polling")
	flag.Parse()
	logger := log.New(os.Stderr, "mediashelf-agent: ", log.LstdFlags|log.Lmsgprefix)
	c, err := agent.LoadConfig(*configPath)
	if err != nil {
		logger.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	runner := agent.NewRunner(c, logger)
	if err := runner.Run(ctx, *once, *scan); err != nil && err != context.Canceled {
		logger.Fatal(err)
	}
}
