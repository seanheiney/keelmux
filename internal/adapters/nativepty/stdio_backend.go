package nativepty

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
)

var _ ps.Supervisor = (*Backend)(nil)

// These package-private seams are replaced only in the test binary. Installed
// helpers have no option, environment variable or wire operation to bypass them.
var stdioImageAvailable = stdioPlatformImageAvailable
var stdioImageVerifier = verifyStdioRunningImage

// CheckStdioReady reports known platform support only. It does not qualify a
// native executable, launch anything, or create state. Launch repeats this check.
func (b *Backend) CheckStdioReady() error {
	if !stdioImageAvailable() {
		return ps.ErrUnavailable
	}
	return nil
}

func stdioLaunchDigest(spec ps.LaunchSpec) string {
	data, _ := json.Marshal(spec)
	sum := sha256.Sum256(append([]byte("keelmux-stdio-launch-v1\x00"), data...))
	return hex.EncodeToString(sum[:])
}
func validStdioReceipt(r receipt) bool {
	if r.Stdio == nil || r.Stdio.Version != ps.Version || ps.ValidateLaunchSpec(r.Stdio.Spec) != nil || r.Stdio.Spec.SchemaSHA256 != ps.CodecSchemaSHA256 {
		return false
	}
	s, c := r.Stdio.Spec, r.Stdio.Ref
	return c.Runtime == r.Ref && c.PrimaryIncarnation == s.Binding.PrimaryIncarnation && ps.ValidateCursor(ps.Genesis(c.ChannelIncarnation)) == nil && c.Runtime.ExecutionID == s.Binding.ExecutionID && c.Runtime.OperationID == s.Binding.StartOperationID && r.SessionID == s.Binding.SessionID && r.HostID == s.Binding.HostID && r.Executable == s.Binary && r.Digest == stdioLaunchDigest(s)
}
func stdioEnvironment(e ps.CleanEnvironment) []string {
	env := []string{"HOME=" + e.Home, "CODEX_HOME=" + e.CodexHome, "XDG_CONFIG_HOME=" + e.XDGConfigHome, "XDG_CACHE_HOME=" + e.XDGCacheHome, "XDG_DATA_HOME=" + e.XDGDataHome, "TMPDIR=" + e.TempDir}
	if e.Path != "" {
		env = append(env, "PATH="+e.Path)
	}
	if e.Locale != "" {
		env = append(env, "LANG="+e.Locale, "LC_ALL="+e.Locale)
	}
	return env
}

type stdioDirectory struct {
	Path          string
	Device, Inode uint64
}

func stdioDirectories(spec ps.LaunchSpec) ([]stdioDirectory, error) {
	e := spec.Environment
	paths := []string{e.Home, e.CodexHome, e.XDGConfigHome, e.XDGCacheHome, e.XDGDataHome, e.TempDir, spec.Workspace}
	out := make([]stdioDirectory, 0, len(paths))
	for n, p := range paths {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsAny(p, "\x00\r\n") {
			return nil, ps.ErrInvalid
		}
		canonical, err := filepath.EvalSymlinks(p)
		if err != nil || canonical != p {
			return nil, ps.ErrConflict
		}
		i, err := os.Lstat(p)
		if err != nil || !i.IsDir() || !owned(i) || (n < 6 && i.Mode().Perm() != 0700) {
			return nil, ps.ErrConflict
		}
		st, ok := i.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, ps.ErrUnavailable
		}
		out = append(out, stdioDirectory{p, uint64(st.Dev), st.Ino})
	}
	return out, nil
}
func sameStdioDirectories(spec ps.LaunchSpec, want []stdioDirectory) bool {
	got, err := stdioDirectories(spec)
	if err != nil || len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
func stdioFileDigest(path string) (string, error) {
	f, err := openStdioExecutable(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", ps.ErrUnavailable
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (b *Backend) LaunchStdio(ctx context.Context, spec ps.LaunchSpec) (ps.ChannelRef, error) {
	if ps.ValidateLaunchSpec(spec) != nil || spec.SchemaSHA256 != ps.CodecSchemaSHA256 {
		return ps.ChannelRef{}, ps.ErrInvalid
	}
	for _, id := range []domain.ID{spec.Binding.HostID, spec.Binding.SessionID, spec.Binding.ExecutionID, spec.Binding.StartOperationID} {
		if !validID(id) {
			return ps.ChannelRef{}, ps.ErrInvalid
		}
	}
	// Known unsupported running-image proof refuses BEFORE receipt/helper/child.
	if b.CheckStdioReady() != nil {
		return ps.ChannelRef{}, ps.ErrUnavailable
	}
	dirs, err := stdioDirectories(spec)
	if err != nil {
		return ps.ChannelRef{}, err
	}
	digest, err := stdioFileDigest(spec.Binary)
	if err != nil || digest != spec.BinarySHA256 {
		return ps.ChannelRef{}, ps.ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	unlock, err := fileLock(ctx, filepath.Join(b.state, "launch.lock"))
	if err != nil {
		return ps.ChannelRef{}, err
	}
	defer unlock()
	r, err := b.readReceipt(spec.Binding.ExecutionID)
	if err == nil {
		if r.Mode != "stdio" || r.Stdio == nil || r.Digest != stdioLaunchDigest(spec) {
			return ps.ChannelRef{}, ps.ErrConflict
		}
		if _, e := b.readStdio(r); e != nil {
			return r.Stdio.Ref, e
		}
		o, e := b.observe(ctx, r)
		if e != nil {
			return r.Stdio.Ref, e
		}
		if !o.Alive {
			return r.Stdio.Ref, ps.ErrUncertain
		}
		return r.Stdio.Ref, nil
	}
	if !os.IsNotExist(err) {
		return ps.ChannelRef{}, err
	}
	helper, err := exec.LookPath(b.helper)
	if err != nil {
		return ps.ChannelRef{}, ps.ErrUnavailable
	}
	helper, err = filepath.Abs(helper)
	if err != nil {
		return ps.ChannelRef{}, err
	}
	boot, err := bootID()
	if err != nil {
		return ps.ChannelRef{}, err
	}
	binding := spec.Binding
	ref := domain.RuntimeRef{Backend: "native", Socket: b.socket(binding.ExecutionID), Session: nonce(), BootID: boot, ExecutionID: binding.ExecutionID, OperationID: binding.StartOperationID}
	channel := ps.Channel{Version: ps.Version, Spec: spec, Ref: ps.ChannelRef{Runtime: ref, PrimaryIncarnation: binding.PrimaryIncarnation, ChannelIncarnation: domain.ID(nonce()[:32])}}
	env := stdioEnvironment(spec.Environment)
	r = receipt{Version: 2, Executable: spec.Binary, EnvDigest: environmentDigest(env), SessionID: binding.SessionID, HostID: binding.HostID, Digest: stdioLaunchDigest(spec), Auth: nonce(), LinkToken: nonce(), Mode: "stdio", Stage: "intent", Ref: ref, Stdio: &channel}
	state := stdioState{Version: ps.Version, Channel: channel, Directories: dirs, Bootstrap: ps.BootstrapUnsent, Continuity: ps.ContinuityComplete, Cursor: ps.Genesis(channel.Ref.ChannelIncarnation), Ack: ps.Genesis(channel.Ref.ChannelIncarnation)}
	// An orphan sidecar is not a launch capability; missing receipt never repairs
	// it. Refuse collision, then commit sidecar before the launch intent.
	if _, e := os.Lstat(b.stdioPath(binding.ExecutionID)); !os.IsNotExist(e) {
		return ps.ChannelRef{}, ps.ErrConflict
	}
	if err = b.saveStdio(state); err != nil {
		return channel.Ref, err
	}
	if err = b.writeReceipt(r); err != nil {
		return channel.Ref, err
	}
	rd, wr, err := os.Pipe()
	if err != nil {
		return channel.Ref, err
	}
	defer rd.Close()
	defer wr.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return channel.Ref, err
	}
	defer null.Close()
	cmd := exec.Command(helper, "serve", "--state", b.state, "--execution", string(binding.ExecutionID))
	cmd.Stdin = rd
	cmd.Stdout = null
	cmd.Stderr = null
	// The helper itself must not inherit loader/proxy/capability variables.
	cmd.Env = append([]string(nil), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return channel.Ref, ps.ErrUncertain
	}
	go func() { _ = cmd.Wait() }()
	_ = rd.Close()
	launch := domain.LaunchSpec{Session: domain.Session{ID: binding.SessionID, HostID: binding.HostID, Workspace: spec.Workspace, Mode: domain.Managed, Execution: domain.Execution{ID: binding.ExecutionID, Generation: binding.Generation, Provider: "codex", ProviderVersion: "0.159.0"}}, OperationID: binding.StartOperationID, Argv: []string{spec.Binary, "app-server"}}
	if err = json.NewEncoder(wr).Encode(launchMessage{Spec: launch, Auth: r.Auth, Executable: spec.Binary, Env: env, HistoryBytes: b.history, Stdio: &spec}); err != nil {
		return channel.Ref, ps.ErrUncertain
	}
	_ = wr.Close()
	for {
		latest, e := b.readReceipt(binding.ExecutionID)
		if e != nil {
			return channel.Ref, e
		}
		r = latest
		if r.Stage == "running" {
			o, e := b.observe(ctx, r)
			if e == nil && o.Alive {
				return r.Stdio.Ref, nil
			}
		}
		if r.Stage == "failed" || r.Stage == "exited" || r.Stage == "stopped" {
			return r.Stdio.Ref, ps.ErrUncertain
		}
		select {
		case <-ctx.Done():
			return r.Stdio.Ref, ps.ErrUncertain
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (b *Backend) stdioReceipt(ref ps.ChannelRef) (receipt, error) {
	r, err := b.receiptForRef(ref.Runtime)
	if err != nil {
		return r, err
	}
	if r.Mode != "stdio" || r.Stdio == nil || r.Stdio.Ref != ref {
		return r, ps.ErrConflict
	}
	return r, nil
}
func stdioError(code string) error {
	switch code {
	case "":
		return nil
	case "invalid":
		return ps.ErrInvalid
	case "conflict":
		return ps.ErrConflict
	case "storage":
		return ps.ErrStorage
	case "uncertain":
		return ps.ErrUncertain
	default:
		return ps.ErrUnavailable
	}
}
func stdioErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ps.ErrInvalid):
		return "invalid"
	case errors.Is(err, ps.ErrConflict):
		return "conflict"
	case errors.Is(err, ps.ErrStorage):
		return "storage"
	case errors.Is(err, ps.ErrUncertain):
		return "uncertain"
	default:
		return "unavailable"
	}
}
