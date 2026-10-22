//go:build windows

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// launchTool starts only the locally configured executable and inherits the
// agent's current Windows session. A service running in session 0 cannot show
// a GUI in a different logged-in user's session.
func launchTool(tool Tool) error {
	ext := strings.ToLower(filepath.Ext(tool.Path))
	if ext != ".exe" && ext != ".com" && ext != ".cmd" && ext != ".bat" {
		return fmt.Errorf("unsupported Windows tool type %q", ext)
	}
	wd := tool.WorkingDir
	if wd == "" {
		wd = filepath.Dir(tool.Path)
	}
	var exe string
	var args []string
	if ext == ".cmd" || ext == ".bat" {
		// cmd.exe expands percent and delayed-expansion syntax even inside quotes.
		// Refuse those characters so a local path cannot alter the wrapper command.
		if strings.ContainsAny(tool.Path, "%!^&|<>()\r\n\x00\"") {
			return fmt.Errorf("script path contains command-interpreter metacharacters")
		}
		systemRoot := os.Getenv("SystemRoot")
		if systemRoot == "" {
			systemRoot = `C:\Windows`
		}
		exe = filepath.Join(systemRoot, "System32", "cmd.exe")
		args = []string{exe, "/d", "/s", "/c", `""` + tool.Path + `""`}
	} else {
		exe = tool.Path
		args = []string{tool.Path}
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("tool executable is unavailable: %w", err)
	}
	proc, err := os.StartProcess(exe, args, &os.ProcAttr{Dir: wd, Env: os.Environ(), Sys: &syscall.SysProcAttr{}})
	if err != nil {
		return err
	}
	return proc.Release()
}
