//go:build darwin || linux

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

func checkStdio(ctx context.Context, argv []string, executable string, selectedEnv map[string]string, dir string, r Receipt) (result Receipt) {
	result = r
	if ctx.Err() != nil {
		result.Code = "cancelled"
		return
	}
	private, err := os.MkdirTemp("", "keelmux-tool-health-")
	if err != nil {
		result.Code = "private_directory_failed"
		return
	}
	result.Cleanup = "complete"
	defer func() {
		if os.RemoveAll(private) != nil {
			result.Cleanup, result.Code = "failed", "cleanup_failed"
			result.Health = "unhealthy"
		}
	}()
	env := map[string]string{"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"}
	for key, value := range selectedEnv {
		env[key] = value
	}
	for _, folder := range []string{"home", "tmp", "cache", "config", "data", "state"} {
		if os.Mkdir(filepath.Join(private, folder), 0700) != nil {
			result.Code = "private_directory_failed"
			return
		}
	}
	for key, folder := range map[string]string{"HOME": "home", "USERPROFILE": "home", "TMPDIR": "tmp", "TMP": "tmp", "TEMP": "tmp", "XDG_CACHE_HOME": "cache", "XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state", "APPDATA": "config", "LOCALAPPDATA": "data"} {
		env[key] = filepath.Join(private, folder)
	}
	childIn, parentIn, err := os.Pipe()
	if err != nil {
		result.Code = "pipe_failed"
		return
	}
	defer childIn.Close()
	defer parentIn.Close()
	parentOut, childOut, err := os.Pipe()
	if err != nil {
		result.Code = "pipe_failed"
		return
	}
	defer parentOut.Close()
	defer childOut.Close()
	stderr, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		result.Code = "pipe_failed"
		return
	}
	defer stderr.Close()
	cmd := exec.Command(executable, argv[1:]...)
	cmd.Dir = dir
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	sort.Strings(cmd.Env)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = childIn, childOut, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if ctx.Err() != nil {
		result.Code = "cancelled"
		return
	}
	if cmd.Start() != nil {
		result.Code = "start_failed"
		return
	}
	result.Started = true
	childIn.Close()
	childOut.Close()
	defer func() {
		parentIn.Close()
		// The direct child is not reaped until after the final group signal.
		// Its unreaped PID anchors the fresh group ID against reuse. This is an
		// in-memory child handle, never a saved PID or an arbitrary group.
		time.Sleep(25 * time.Millisecond)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		time.Sleep(100 * time.Millisecond)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		// Darwin may return EPERM for a group containing only zombies. Verify
		// that exact still-anchored group's kernel state before accepting it.
		stopped := groupStopped(cmd.Process.Pid)
		deadline := time.Now().Add(250 * time.Millisecond)
		for !stopped && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
			stopped = groupStopped(cmd.Process.Pid)
		}
		if !stopped {
			result.Cleanup = "failed"
		}
		parentOut.Close()
		waited := make(chan struct{})
		go func() { _ = cmd.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(time.Second):
			result.Cleanup = "failed"
		}
		if result.Cleanup == "failed" {
			result.Code, result.Health = "cleanup_failed", "unhealthy"
		}
	}()
	// Closing our pipe endpoints releases a blocked read or write on deadline;
	// shutdown still runs on every path and has its own finite bound.
	stop := context.AfterFunc(ctx, func() { parentIn.Close(); parentOut.Close() })
	defer stop()
	result.Code = handshake(parentIn, parentOut, &result)
	if ctx.Err() != nil {
		result.Code = "cancelled_or_timeout"
	}
	if result.Code == "health_ok" {
		result.Health = "healthy"
	}
	return
}
