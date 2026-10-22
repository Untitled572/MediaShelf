//go:build !windows

package agent

import "os"

func replaceJournalFile(src, dst string) error { return os.Rename(src, dst) }
