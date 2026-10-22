//go:build !windows

package agent

import "fmt"

func launchTool(Tool) error { return fmt.Errorf("tool launching is supported only on Windows") }
