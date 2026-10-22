//go:build windows

package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLaunchBatchFileUsesSafeWrapperAndRuns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "media shelf agent test")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "started.txt")
	script := filepath.Join(dir, "safe-agent-test.bat")
	body := "@echo off\r\necho started>\"" + marker + "\"\r\n"
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := launchTool(Tool{Path: script, WorkingDir: dir}); err != nil {
		t.Fatalf("launch batch: %v", err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatal("batch wrapper did not create its harmless marker")
}
