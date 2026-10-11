package tools

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Inspect the freshly owned group while its direct leader remains unreaped.
// /proc observations authorize no signals and are never persisted in receipts.
func groupStopped(group int) bool {
	dir, err := os.Open("/proc")
	if err != nil {
		return false
	}
	entries, err := dir.ReadDir(32769)
	dir.Close()
	if (err != nil && err != io.EOF) || len(entries) > 32768 {
		return false
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false
		}
		// comm may contain spaces or parentheses; the last ')' terminates it.
		end := strings.LastIndexByte(string(raw), ')')
		if end < 0 {
			return false
		}
		fields := strings.Fields(string(raw[end+1:]))
		if len(fields) < 3 {
			return false
		}
		pgid, err := strconv.Atoi(fields[2])
		if err != nil {
			return false
		}
		if pgid == group && fields[0] != "Z" && fields[0] != "X" && fields[0] != "x" {
			return false
		}
	}
	return true
}
