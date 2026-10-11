package diagnostics

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var versionRE = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

type capture struct {
	bytes.Buffer
	overflow bool
}

func (c *capture) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 4096 - c.Len()
	if len(p) > remaining {
		c.overflow = true
		p = p[:max(0, remaining)]
	}
	c.Buffer.Write(p)
	return n, nil
}

func providerChecks(ctx context.Context, probe bool) []Check {
	var result []Check
	for _, name := range []string{"codex", "claude"} {
		c := Check{Name: "provider_" + name, State: "missing", Summary: "Supported CLI is not executable on PATH.", Action: "Install the provider through its native instructions, then rerun diagnostics."}
		path, err := exec.LookPath(name)
		if err == nil {
			path, err = filepath.EvalSymlinks(path)
		}
		if err == nil {
			var info os.FileInfo
			info, err = os.Stat(path)
			if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0) {
				err = os.ErrPermission
			}
		}
		if err != nil {
			result = append(result, c)
			continue
		}
		c.State = "available"
		c.Summary = "Executable is available on PATH; version was not probed."
		c.Action = "Use diagnose --probe-versions for a bounded native --version check."
		if probe {
			v, e := probeVersion(ctx, path, name)
			c.Action = ""
			if e != nil {
				c.State = "unavailable"
				c.Summary = "Native version check failed or returned an unsupported format."
				c.Action = "Inspect the native installation; no login, upgrade or model request was attempted."
			} else {
				c.State = "observed"
				c.Summary = "Native --version reported " + v + "; this is not model, auth or terminal-readiness proof."
				at := time.Now().UTC()
				evidence(&c, at, at)
			}
		}
		result = append(result, c)
	}
	return append(result, Check{Name: "provider_grok", State: "unsupported", Summary: "Native launch and version semantics remain unqualified."})
}

func probeVersion(ctx context.Context, path, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Stdin = nil
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	var out capture
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	if out.overflow {
		return "", os.ErrInvalid
	}
	v := strings.TrimSpace(out.String())
	if name == "codex" {
		v = strings.TrimPrefix(v, "codex-cli ")
	} else {
		v = strings.TrimSuffix(v, " (Claude Code)")
	}
	if !versionRE.MatchString(v) {
		return "", os.ErrInvalid
	}
	return v, nil
}
