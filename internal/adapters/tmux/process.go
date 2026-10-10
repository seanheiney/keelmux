package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

func bootID(ctx context.Context) (string, error) {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		return strings.TrimSpace(string(data)), err
	}
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(ctx, commandTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "kern.boottime").Output()
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(string(out))
		// sysctl appends a locale-dependent human date; use only sec/usec.
		if end := strings.Index(value, "}"); end >= 0 {
			return "darwin:" + value[:end+1], nil
		}
		return "", fmt.Errorf("unrecognized kernel boot evidence")
	}
	return "", fmt.Errorf("tmux process evidence unsupported on %s", runtime.GOOS)
}

func processStart(ctx context.Context, pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid process PID")
	}
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return "", err
		}
		// The command field may contain spaces and parentheses. Field 22 is
		// starttime in host boot clock ticks, after the final command ')'.
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			return "", fmt.Errorf("invalid proc process evidence")
		}
		fields := strings.Fields(string(data)[end+1:])
		if len(fields) < 20 || fields[0] == "Z" {
			return "", fmt.Errorf("process unavailable")
		}
		return "linux-ticks:" + fields[19], nil
	}
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(ctx, commandTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "stat=")
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		fields := strings.Fields(string(out))
		if len(fields) != 6 || strings.Contains(fields[5], "Z") {
			return "", fmt.Errorf("process unavailable")
		}
		return "darwin-lstart:" + strings.Join(fields[:5], " "), nil
	}
	return "", fmt.Errorf("process evidence unsupported on %s", runtime.GOOS)
}
