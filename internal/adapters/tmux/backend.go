// Package tmux owns native terminals on one explicit private tmux socket.
// It supplies lifecycle and attachment, never semantic prompt delivery.
package tmux

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	bpruntime "github.com/seanheiney/keelmux/internal/runtime"
)

const commandTimeout = 5 * time.Second

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Backend struct {
	socket, state, config, binary, owner string
	runCommand                           func(context.Context, ...string) ([]byte, error)
}

var _ bpruntime.Backend = (*Backend)(nil)

type receipt struct {
	Version   int               `json:"version"`
	SessionID domain.ID         `json:"session_id"`
	HostID    domain.ID         `json:"host_id"`
	Digest    string            `json:"launch_digest"`
	Nonce     string            `json:"nonce"`
	LinkToken string            `json:"link_token"`
	Stage     string            `json:"stage"`
	Ref       domain.RuntimeRef `json:"ref"`
}

// New never discovers or connects to the user's default tmux server. The
// socket's parent must already be private. An existing server requires our
// durable ownership receipt and matching server marker.
func New(socket string) (*Backend, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || strings.ContainsAny(socket, "\x00\n\r") {
		return nil, errors.New("tmux requires an explicit clean absolute socket path")
	}
	if len(socket) >= 104 {
		return nil, errors.New("tmux socket path exceeds portable Unix socket limit")
	}
	if err := privateDirectory(filepath.Dir(socket)); err != nil {
		return nil, err
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		return nil, fmt.Errorf("tmux unavailable: %w", err)
	}
	b := &Backend{socket: socket, state: socket + ".bp", config: socket + ".bp/config", binary: binary}
	if err := os.Mkdir(b.state, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	if err := privateDirectory(b.state); err != nil {
		return nil, err
	}
	b.runCommand = b.command
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	unlock, err := b.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	ownerPath := filepath.Join(b.state, "owner")
	owner, err := readPrivate(ownerPath)
	if os.IsNotExist(err) {
		if _, socketErr := os.Lstat(socket); !os.IsNotExist(socketErr) {
			return nil, errors.New("refusing existing socket without Backplane ownership receipt")
		}
		b.owner = nonce()
		if err := atomicWrite(ownerPath, []byte(b.owner)); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		b.owner = string(owner)
	}
	if !identityPattern.MatchString(b.owner) {
		return nil, errors.New("invalid tmux ownership receipt")
	}
	config := "set-option -g @bp_owner " + b.owner + "\n" +
		"set-option -g default-shell /bin/sh\nset-option -g default-terminal tmux-256color\n" +
		"set-option -g prefix C-g\nset-option -g prefix2 None\nset-option -g status off\n" +
		"set-option -g destroy-unattached off\nset-option -g update-environment ''\n" +
		"set-option -gw remain-on-exit on\nunbind-key -a\n" +
		"bind-key -T prefix d detach-client\nbind-key -T prefix C-g send-prefix\n"
	if err := atomicWrite(b.config, []byte(config)); err != nil {
		return nil, err
	}
	if err := b.verifyServer(ctx); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Backend) Launch(ctx context.Context, spec domain.LaunchSpec) (domain.RuntimeRef, error) {
	if err := validateSpec(spec); err != nil {
		return domain.RuntimeRef{}, err
	}
	unlock, err := b.lock(ctx)
	if err != nil {
		return domain.RuntimeRef{}, err
	}
	defer unlock()
	digest := launchDigest(spec)
	r, err := b.readReceipt(spec.Session.Execution.ID)
	if err == nil {
		if r.Ref.OperationID != spec.OperationID || r.Digest != digest {
			return domain.RuntimeRef{}, conflict("execution already belongs to another launch")
		}
		o, err := b.observe(ctx, r)
		if err != nil {
			return r.Ref, err
		}
		if o.Alive {
			return o.Ref, nil
		}
		return o.Ref, conflict("launch was already attempted: " + o.Detail + "; inspect or explicitly stop; no automatic replay")
	}
	if !os.IsNotExist(err) {
		return domain.RuntimeRef{}, err
	}
	if err := b.verifyServer(ctx); err != nil {
		return domain.RuntimeRef{}, err
	}
	boot, err := bootID(ctx)
	if err != nil {
		return domain.RuntimeRef{}, err
	}
	key := executionKey(spec.Session.Execution.ID)
	name := "bp_" + key[:24]
	r = receipt{Version: 1, SessionID: spec.Session.ID, HostID: spec.Session.HostID, Digest: digest, Nonce: nonce(), LinkToken: nonce(), Stage: "starting",
		Ref: domain.RuntimeRef{Backend: "tmux", Socket: b.socket, Session: name, BootID: boot, ExecutionID: spec.Session.Execution.ID, OperationID: spec.OperationID}}
	// The tombstone precedes every possible external effect and is never removed
	// by Stop or runtime loss. An attempted execution cannot be launched again.
	if err := b.writeReceipt(r); err != nil {
		return r.Ref, err
	}
	launcher, gate, started := b.launchPaths(r.Ref.ExecutionID)
	env := b.managedEnv(spec, r.LinkToken)
	if err := atomicWrite(launcher, []byte(launcherScript(spec, env, gate, started))); err != nil {
		return r.Ref, err
	}
	// User env is exported only by the quoted launcher: tmux's command-list
	// parser treats a trailing semicolon specially even in an argv element.
	// The native workspace is selected by that launcher before activation.
	args := []string{"new-session", "-d", "-s", name, "-c", "/"}
	// Multiple shell-command arguments are a documented direct exec path. No
	// user argv ever enters the tmux command parser; it is quoted in the owned
	// script, behind a bounded gate, and exec retains the pane process identity.
	args = append(args, "/bin/sh", launcher)
	for key, value := range r.markers() {
		args = append(args, ";", "set-option", "-t", name, key, value)
		args = append(args, ";", "set-option", "-p", "-t", "="+name+":0.0", key, value)
	}
	if _, err := b.runCommand(ctx, args...); err != nil {
		return r.Ref, fmt.Errorf("tmux launch acknowledgement uncertain; gated startup tombstone retained: %w", err)
	}
	ref, dead, err := b.pane(ctx, "="+name)
	if err != nil || dead {
		return r.Ref, fmt.Errorf("gated startup could not be identified: %v", err)
	}
	ref.Backend, ref.Socket, ref.BootID, ref.ExecutionID, ref.OperationID = "tmux", b.socket, boot, r.Ref.ExecutionID, r.Ref.OperationID
	ref.ProcessStart, err = processStart(ctx, ref.PID)
	if err != nil {
		return r.Ref, fmt.Errorf("gated pane process evidence: %w", err)
	}
	r.Ref = ref
	for _, target := range [][]string{{"set-option", "-t", ref.Session}, {"set-option", "-p", "-t", ref.Pane}} {
		if _, err := b.runCommand(ctx, append(target, "@bp_start", ref.ProcessStart)...); err != nil {
			return r.Ref, err
		}
	}
	if err := b.writeReceipt(r); err != nil {
		return r.Ref, err
	}
	// Record authorization to activate before releasing the gate. Recovery only
	// observes this record; it never releases an unreleased gate or starts again.
	r.Stage = "released"
	if err := b.writeReceipt(r); err != nil {
		return r.Ref, err
	}
	if err := atomicWrite(gate, []byte("go\n")); err != nil {
		return r.Ref, err
	}
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return r.Ref, fmt.Errorf("activation acknowledgement uncertain: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
		if _, dead, err := b.pane(ctx, ref.Pane); err != nil || dead {
			return r.Ref, errors.New("native program did not acknowledge gate activation")
		}
	}
	_ = os.Remove(launcher) // Native credentials supplied in Env are not retained.
	o, err := b.observe(ctx, r)
	if err != nil {
		return r.Ref, err
	}
	if !o.Alive {
		return r.Ref, fmt.Errorf("native program is unavailable: %s", o.Detail)
	}
	return o.Ref, nil
}

func (b *Backend) Lookup(ctx context.Context, execution, operation domain.ID) (bpruntime.Observation, error) {
	if !validID(execution) || !validID(operation) {
		return bpruntime.Observation{}, errors.New("lookup requires exact execution and operation IDs")
	}
	r, err := b.readReceipt(execution)
	if os.IsNotExist(err) {
		return bpruntime.Observation{Detail: "No recorded tmux launch; absence does not authorize replay."}, nil
	}
	if err != nil {
		return bpruntime.Observation{}, err
	}
	if r.Ref.OperationID != operation {
		return bpruntime.Observation{}, conflict("operation does not match recorded execution")
	}
	return b.observe(ctx, r)
}

func (b *Backend) Inspect(ctx context.Context, ref domain.RuntimeRef) (bpruntime.Observation, error) {
	r, err := b.receiptForRef(ref)
	if err != nil {
		return bpruntime.Observation{}, err
	}
	return b.observe(ctx, r)
}

// ValidateLink requires inherited launch authority and independently verifies
// the live pane's exact identity. It does not identify account or native thread.
// Host owners can read their own processes/state; this is no OS sandbox.
func (b *Backend) ValidateLink(ctx context.Context, sessionID, executionID, operationID domain.ID, token string) error {
	if !validID(sessionID) || !validID(executionID) || !validID(operationID) || !identityPattern.MatchString(token) {
		return conflict("managed link requires exact inherited launch context")
	}
	r, err := b.readReceipt(executionID)
	if err != nil {
		return err
	}
	if r.SessionID != sessionID || r.Ref.OperationID != operationID || subtle.ConstantTimeCompare([]byte(r.LinkToken), []byte(token)) != 1 {
		return conflict("managed link launch authority does not match")
	}
	o, err := b.observe(ctx, r)
	if err != nil {
		return err
	}
	if !o.Alive {
		return conflict("managed link runtime unavailable: " + o.Detail)
	}
	return nil
}

func (b *Backend) Stop(ctx context.Context, ref domain.RuntimeRef) error {
	unlock, err := b.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := b.receiptForRef(ref)
	if err != nil {
		return err
	}
	o, err := b.observe(ctx, r)
	if err != nil {
		return err
	}
	// Pending launches have a valid receipt but may never have installed all
	// markers. Do not kill an unverified locator merely because its name matches.
	if o.Detail == "Runtime is missing; attempted-launch tombstone retained." || o.Detail == "Host boot changed; old execution cannot survive." {
		r.Stage = "stopped"
		return b.writeReceipt(r)
	}
	if _, err := b.runCommand(ctx, "kill-session", "-t", r.Ref.Session); err != nil {
		return err
	}
	r.Stage = "stopped"
	launcher, gate, _ := b.launchPaths(ref.ExecutionID)
	_ = os.Remove(launcher)
	_ = os.Remove(gate)
	return b.writeReceipt(r)
}

func (b *Backend) AttachCommand(ref domain.RuntimeRef, readOnly bool) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	o, err := b.Inspect(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !o.Alive {
		return nil, conflict("native terminal unavailable: " + o.Detail)
	}
	args := []string{b.binary, "-N", "-S", b.socket, "attach-session", "-E"}
	if readOnly {
		args = append(args, "-r")
	}
	return append(args, "-t", ref.Session), nil
}

func (b *Backend) observe(ctx context.Context, r receipt) (bpruntime.Observation, error) {
	o := bpruntime.Observation{Ref: r.Ref}
	boot, err := bootID(ctx)
	if err != nil {
		return o, err
	}
	if boot != r.Ref.BootID {
		o.Detail = "Host boot changed; old execution cannot survive."
		return o, nil
	}
	if err := b.verifyServer(ctx); err != nil {
		return o, err
	}
	if _, err := os.Lstat(b.socket); os.IsNotExist(err) {
		o.Detail = "Runtime is missing; attempted-launch tombstone retained."
		return o, nil
	}
	target := r.Ref.Pane
	if target == "" {
		target = "=" + r.Ref.Session
	}
	ref, dead, err := b.pane(ctx, target)
	if err != nil {
		if missingTarget(err) {
			o.Detail = "Runtime is missing; attempted-launch tombstone retained."
			return o, nil
		}
		return o, err
	}
	if r.Ref.Pane != "" && (ref.Session != r.Ref.Session || ref.Pane != r.Ref.Pane || ref.PID != r.Ref.PID) {
		return o, conflict("pane locators no longer match recorded runtime")
	}
	for key, want := range r.markers() {
		got, err := b.runCommand(ctx, "show-options", "-p", "-v", "-t", ref.Pane, key)
		if err != nil || strings.TrimSpace(string(got)) != want {
			return o, conflict("runtime marker mismatch: " + key)
		}
		got, err = b.runCommand(ctx, "show-options", "-v", "-t", ref.Session, key)
		if err != nil || strings.TrimSpace(string(got)) != want {
			return o, conflict("session marker mismatch: " + key)
		}
	}
	if r.Ref.ProcessStart != "" {
		got, err := b.runCommand(ctx, "show-options", "-p", "-v", "-t", ref.Pane, "@bp_start")
		if err != nil || strings.TrimSpace(string(got)) != r.Ref.ProcessStart {
			return o, conflict("process-start marker mismatch")
		}
		got, err = b.runCommand(ctx, "show-options", "-v", "-t", ref.Session, "@bp_start")
		if err != nil || strings.TrimSpace(string(got)) != r.Ref.ProcessStart {
			return o, conflict("session process-start marker mismatch")
		}
	}
	clients, err := b.runCommand(ctx, "list-clients", "-t", ref.Session, "-F", "#{client_readonly}")
	if err != nil {
		return o, err
	}
	for _, line := range strings.Fields(string(clients)) {
		if line != "0" && line != "1" {
			return o, errors.New("unrecognized tmux client writability")
		}
		if line == "0" {
			o.WritableClients++
		}
	}
	if dead {
		o.Detail = "Native pane exited; attempted-launch tombstone retained."
		return o, nil
	}
	if r.Ref.ProcessStart == "" || r.Stage == "starting" {
		o.Detail = "Startup incomplete; native program was not released. Inspect or stop explicitly; no automatic replay."
		return o, nil
	}
	start, err := processStart(ctx, ref.PID)
	if err != nil || start != r.Ref.ProcessStart {
		return o, conflict("pane process lifetime does not match launch evidence")
	}
	_, _, started := b.launchPaths(r.Ref.ExecutionID)
	if _, err := os.Stat(started); err != nil {
		o.Detail = "Activation uncertain; no native gate acknowledgement. Inspect or stop explicitly; no automatic replay."
		return o, nil
	}
	if r.Stage == "stopped" {
		return o, conflict("stopped execution unexpectedly has a live pane")
	}
	o.Alive = true
	o.Detail = "Owned native terminal; Ctrl-G d detaches, Ctrl-G Ctrl-G sends literal Ctrl-G. Account/model/turn acceptance unknown."
	return o, nil
}

func (b *Backend) pane(ctx context.Context, target string) (domain.RuntimeRef, bool, error) {
	out, err := b.runCommand(ctx, "list-panes", "-s", "-t", target, "-F", "#{session_id}\t#{pane_id}\t#{pane_pid}\t#{pane_dead}")
	if err != nil {
		return domain.RuntimeRef{}, false, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		return domain.RuntimeRef{}, false, conflict("managed session must contain exactly one pane")
	}
	fields := strings.Split(lines[0], "\t")
	if len(fields) != 4 {
		return domain.RuntimeRef{}, false, errors.New("invalid tmux pane evidence")
	}
	pid, err := strconv.Atoi(fields[2])
	if err != nil || pid <= 0 {
		return domain.RuntimeRef{}, false, errors.New("invalid pane PID")
	}
	return domain.RuntimeRef{Session: fields[0], Pane: fields[1], PID: pid}, fields[3] == "1", nil
}

func (b *Backend) verifyServer(ctx context.Context) error {
	info, err := os.Lstat(b.socket)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 || !owned(info) {
		return errors.New("tmux socket is not private and owned")
	}
	out, err := b.runCommand(ctx, "show-options", "-g", "-v", "@bp_owner")
	if err != nil {
		if missingTarget(err) {
			return nil
		} // A stale owned socket is not live.
		return err
	}
	if strings.TrimSpace(string(out)) != b.owner {
		return conflict("tmux server ownership marker mismatch")
	}
	return nil
}

func (b *Backend) receiptForRef(ref domain.RuntimeRef) (receipt, error) {
	if ref.Backend != "tmux" || ref.Socket != b.socket || !validID(ref.ExecutionID) || !validID(ref.OperationID) {
		return receipt{}, conflict("runtime reference is not this backend's exact identity")
	}
	r, err := b.readReceipt(ref.ExecutionID)
	if err != nil {
		return r, err
	}
	if r.Ref != ref {
		return r, conflict("runtime reference differs from durable launch evidence")
	}
	return r, nil
}

func (r receipt) markers() map[string]string {
	return map[string]string{"@bp_session": string(r.SessionID), "@bp_host": string(r.HostID), "@bp_execution": string(r.Ref.ExecutionID), "@bp_operation": string(r.Ref.OperationID), "@bp_boot": r.Ref.BootID, "@bp_nonce": r.Nonce}
}

func (b *Backend) readReceipt(execution domain.ID) (receipt, error) {
	var r receipt
	data, err := readPrivate(filepath.Join(b.state, executionKey(execution)+".json"))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.Version != 1 || r.Ref.ExecutionID != execution || r.Ref.Socket != b.socket || !validID(r.Ref.OperationID) || !validID(r.SessionID) || !validID(r.HostID) || !validID(domain.ID(r.Nonce)) || !validID(domain.ID(r.LinkToken)) {
		return r, errors.New("invalid durable tmux receipt")
	}
	return r, nil
}

func (b *Backend) writeReceipt(r receipt) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(b.state, executionKey(r.Ref.ExecutionID)+".json"), data)
}

func (b *Backend) launchPaths(execution domain.ID) (string, string, string) {
	base := filepath.Join(b.state, executionKey(execution))
	return base + ".sh", base + ".gate", base + ".started"
}

func (b *Backend) command(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	base := []string{"-N", "-f", b.config, "-S", b.socket}
	// Only new-session can start the owned server. Observation/attach must never
	// accidentally create a replacement server after runtime loss.
	if len(args) > 0 && args[0] == "new-session" {
		base = base[1:]
	}
	cmd := exec.CommandContext(ctx, b.binary, append(base, args...)...)
	cmd.Env = cleanTmuxEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func cleanTmuxEnv(env []string) []string {
	clean := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "TMUX=") && !strings.HasPrefix(e, "TMUX_PANE=") {
			clean = append(clean, e)
		}
	}
	return clean
}

func missingTarget(err error) bool {
	s := err.Error()
	for _, detail := range []string{"no server running", "No such file", "can't find", "Connection refused"} {
		if strings.Contains(s, detail) {
			return true
		}
	}
	return false
}

func validateSpec(spec domain.LaunchSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	for _, id := range []domain.ID{spec.Session.ID, spec.Session.HostID, spec.Session.Execution.ID, spec.OperationID} {
		if !validID(id) {
			return errors.New("launch requires bounded literal identity values")
		}
	}
	info, err := os.Stat(spec.Session.Workspace)
	if err != nil || !info.IsDir() {
		return errors.New("launch workspace is not an available directory")
	}
	for _, arg := range spec.Argv {
		if strings.ContainsRune(arg, 0) {
			return errors.New("argv contains NUL")
		}
	}
	for _, env := range spec.Env {
		key, _, ok := strings.Cut(env, "=")
		if !ok || !envPattern.MatchString(key) || strings.ContainsRune(env, 0) {
			return errors.New("invalid launch environment")
		}
		if (strings.HasPrefix(key, "BP_") && key != "BP_STATE_DIR") || key == "TMUX" || key == "TMUX_PANE" || key == "TERM" {
			return errors.New("managed identity and terminal environment cannot be overridden")
		}
	}
	return nil
}

func validID(id domain.ID) bool    { return identityPattern.MatchString(string(id)) }
func conflict(reason string) error { return domain.ConflictError{Reason: reason} }
func executionKey(id domain.ID) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}
func launchDigest(spec domain.LaunchSpec) string {
	value := struct {
		Session, Host, Execution, Operation domain.ID
		Generation                          uint64
		Workspace                           string
		Argv, Env                           []string
	}{spec.Session.ID, spec.Session.HostID, spec.Session.Execution.ID, spec.OperationID, spec.Session.Execution.Generation, spec.Session.Workspace, spec.Argv, spec.Env}
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func nonce() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(bytes[:])
}

func (b *Backend) managedEnv(spec domain.LaunchSpec, token string) []string {
	env := append([]string{}, spec.Env...)
	hasState := false
	for _, value := range env {
		if strings.HasPrefix(value, "BP_STATE_DIR=") {
			hasState = true
		}
	}
	if !hasState {
		env = append(env, "BP_STATE_DIR="+filepath.Dir(b.socket))
	}
	return append(env, "BP_SESSION_ID="+string(spec.Session.ID), "BP_EXECUTION_ID="+string(spec.Session.Execution.ID), "BP_OPERATION_ID="+string(spec.OperationID), "BP_HOST_ID="+string(spec.Session.HostID), "BP_LINK_TOKEN="+token)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func launcherScript(spec domain.LaunchSpec, env []string, gate, started string) string {
	var s strings.Builder
	s.WriteString("#!/bin/sh\nset -eu\n")
	for _, env := range env {
		key, value, _ := strings.Cut(env, "=")
		fmt.Fprintf(&s, "export %s=%s\n", key, shellQuote(value))
	}
	// A crashed captain cannot leave an invisible immortal startup waiter. The
	// pane expires after at most 30 seconds; the durable tombstone remains.
	fmt.Fprintf(&s, "bp_wait=0\nwhile [ ! -f %s ]; do\n  bp_wait=$((bp_wait + 1))\n  if [ \"$bp_wait\" -ge 600 ]; then exit 125; fi\n  sleep 0.05\ndone\n", shellQuote(gate))
	fmt.Fprintf(&s, "cd %s\nprintf 'activated\\n' > %s\nexec", shellQuote(spec.Session.Workspace), shellQuote(started))
	for _, arg := range spec.Argv {
		s.WriteByte(' ')
		s.WriteString(shellQuote(arg))
	}
	s.WriteByte('\n')
	return s.String()
}

func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !owned(info) {
		return fmt.Errorf("tmux directory must be owned and private (0700): %s", path)
	}
	return nil
}

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !owned(info) {
		return nil, fmt.Errorf("runtime state file is not private and owned: %s", path)
	}
	return os.ReadFile(path)
}

func atomicWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (b *Backend) lock(ctx context.Context) (func(), error) {
	file, err := os.OpenFile(filepath.Join(b.state, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !owned(info) || info.Mode().Perm()&0077 != 0 {
		file.Close()
		return nil, errors.New("invalid tmux runtime lock")
	}
	for {
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
		} else if err != syscall.EWOULDBLOCK {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
