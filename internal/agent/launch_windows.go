//go:build windows

package agent

import (
	"fmt"
	"os"
	"os/exec"
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
	exe := tool.Path
	args := append([]string(nil), tool.Args...)
	if ext == ".cmd" || ext == ".bat" {
		// cmd.exe expands percent and delayed-expansion syntax even inside quotes.
		// Refuse these characters so a local path cannot alter the wrapper command.
		if strings.ContainsAny(tool.Path, "%!^&|<>()\r\n\x00\"") {
			return fmt.Errorf("script path contains command-interpreter metacharacters")
		}
		if len(tool.Args) > 0 {
			return fmt.Errorf("arguments for command scripts are not supported")
		}
		systemRoot := os.Getenv("SystemRoot")
		if systemRoot == "" {
			systemRoot = `C:\Windows`
		}
		exe = filepath.Join(systemRoot, "System32", "cmd.exe")
		args = nil
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("tool executable is unavailable: %w", err)
	}
	cmd := exec.Command(exe, args...)
	if ext == ".cmd" || ext == ".bat" {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + exe + `" /d /s /c ""` + tool.Path + `""`}
	}
	cmd.Dir = wd
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
