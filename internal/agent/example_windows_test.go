//go:build windows

package agent

import (
	"path/filepath"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	config, err := LoadConfig(filepath.Join("..", "..", "agent.example.json"))
	if err != nil {
		t.Fatalf("load documented example config: %v", err)
	}
	if len(config.Libraries) == 0 || len(config.Tools) == 0 || len(config.Downloaders) == 0 {
		t.Fatalf("example config is missing documented allowlists: %#v", config)
	}
}
