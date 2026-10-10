// Package nativepty owns persistent Unix process and PTY lifetimes through a
// detached per-execution helper. Control-daemon/client loss never owns the PTY.
// Receipts are tombstones: absent or lost supervisors are never auto-restarted.
package nativepty

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
	"strings"
	"syscall"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	bpruntime "github.com/seanheiney/keelmux/internal/runtime"
)

const DefaultHistoryBytes = 1 << 20
const commandTimeout = 5 * time.Second

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Options controls only local resource/installation choices. Empty HelperPath
// discovers a sibling keelmux-supervisor first, then the executable search path.
type Options struct {
	HelperPath   string
	HistoryBytes int
}
type Backend struct {
	state, helper string
	history       int
}

var _ bpruntime.Backend = (*Backend)(nil)
var _ bpruntime.LinkVerifier = (*Backend)(nil)

type receipt struct {
	Version         int               `json:"version"`
	Executable      string            `json:"executable"`
	EnvDigest       string            `json:"environment_digest"`
	SessionID       domain.ID         `json:"session_id"`
	HostID          domain.ID         `json:"host_id"`
	Digest          string            `json:"launch_digest"`
	Auth            string            `json:"auth"`
	LinkToken       string            `json:"link_token"`
	Mode            string            `json:"mode"`
	Stage           string            `json:"stage"`
	Ref             domain.RuntimeRef `json:"ref"`
	SupervisorPID   int               `json:"supervisor_pid"`
	SupervisorStart string            `json:"supervisor_start"`
	ExitCode        *int              `json:"exit_code,omitempty"`
	Detail          string            `json:"detail,omitempty"`
}
type launchMessage struct {
	Spec             domain.LaunchSpec
	Auth, Executable string
	Env              []string
	HistoryBytes     int
}

func New(stateDir string) (*Backend, error) { return NewWithOptions(stateDir, Options{}) }
func NewWithOptions(stateDir string, options Options) (*Backend, error) {
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir || strings.ContainsAny(stateDir, "\x00\r\n") {
		return nil, errors.New("native supervisor requires a clean absolute private state directory")
	}
	if err := os.Mkdir(stateDir, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	if err := privateDirectory(stateDir); err != nil {
		return nil, err
	}
	// Every execution socket uses a fixed 24-hex basename; reserve its suffix.
	if len(filepath.Join(stateDir, strings.Repeat("a", 24)+".sock")) >= 104 {
		return nil, errors.New("native supervisor state directory exceeds portable Unix socket length")
	}
	if options.HistoryBytes == 0 {
		options.HistoryBytes = DefaultHistoryBytes
	}
	if options.HistoryBytes < 4096 || options.HistoryBytes > 8<<20 {
		return nil, errors.New("native output history must be between 4096 and 8388608 bytes")
	}
	helper := options.HelperPath
	if helper == "" {
		if current, err := os.Executable(); err == nil {
			p := filepath.Join(filepath.Dir(current), "keelmux-supervisor")
			if i, e := os.Stat(p); e == nil && i.Mode().IsRegular() && i.Mode().Perm()&0111 != 0 {
				helper = p
			}
		}
	}
	if helper == "" {
		helper = "keelmux-supervisor"
	}
	// Discovery is deferred until launch/attachment. Inspection and recovery must
	// remain available even when a package upgrade temporarily moves the helper.
	return &Backend{state: stateDir, helper: helper, history: options.HistoryBytes}, nil
}
func (b *Backend) Launch(ctx context.Context, spec domain.LaunchSpec) (domain.RuntimeRef, error) {
	return b.launch(ctx, spec, "pty")
}

// LaunchProcess supervises a foreground service (e.g. Ergo) without allocating a
// terminal. The executable must not daemonize; stdout/stderr are bounded/drained.
func (b *Backend) LaunchProcess(ctx context.Context, spec domain.LaunchSpec) (domain.RuntimeRef, error) {
	return b.launch(ctx, spec, "process")
}
func (b *Backend) launch(ctx context.Context, spec domain.LaunchSpec, mode string) (domain.RuntimeRef, error) {
	if err := validateSpec(spec); err != nil {
		return domain.RuntimeRef{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	unlock, err := fileLock(ctx, filepath.Join(b.state, "launch.lock"))
	if err != nil {
		return domain.RuntimeRef{}, err
	}
	defer unlock()
	digest := launchDigest(spec, mode)
	r, err := b.readReceipt(spec.Session.Execution.ID)
	if err == nil {
		if r.Ref.OperationID != spec.OperationID || r.Digest != digest {
			return r.Ref, conflict("execution already belongs to different launch content or operation")
		}
		o, e := b.observe(ctx, r)
		if e != nil {
			return r.Ref, e
		}
		if o.Alive {
			return o.Ref, nil
		}
		return o.Ref, conflict("launch was already attempted; " + o.Detail + "; no automatic replay")
	}
	if !os.IsNotExist(err) {
		return domain.RuntimeRef{}, err
	}
	helper, err := exec.LookPath(b.helper)
	if err != nil {
		return domain.RuntimeRef{}, fmt.Errorf("keelmux-supervisor unavailable: %w", err)
	}
	executable, err := exec.LookPath(spec.Argv[0])
	if err != nil {
		return domain.RuntimeRef{}, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return domain.RuntimeRef{}, err
	}
	boot, err := bootID()
	if err != nil {
		return domain.RuntimeRef{}, err
	}
	var effectiveEnv []string
	r = receipt{Version: 1, SessionID: spec.Session.ID, HostID: spec.Session.HostID, Digest: digest, Auth: nonce(), LinkToken: nonce(), Mode: mode, Stage: "intent", Ref: domain.RuntimeRef{Backend: "native", Socket: b.socket(spec.Session.Execution.ID), Session: nonce(), BootID: boot, ExecutionID: spec.Session.Execution.ID, OperationID: spec.OperationID}}
	r.Executable = executable
	effectiveEnv = b.managedEnv(spec, r.LinkToken)
	r.EnvDigest = environmentDigest(effectiveEnv)
	// Commit+fsync intent before any helper process or child can exist. A crash
	// after this point is reconciled by inspection, never a second spawn.
	if err := b.writeReceipt(r); err != nil {
		return r.Ref, err
	}
	rd, wr, err := os.Pipe()
	if err != nil {
		return r.Ref, err
	}
	defer rd.Close()
	defer wr.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return r.Ref, err
	}
	defer null.Close()
	cmd := exec.Command(helper, "serve", "--state", b.state, "--execution", string(r.Ref.ExecutionID))
	cmd.Stdin = rd
	cmd.Stdout = null
	cmd.Stderr = null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// No background goroutine retains the caller's stdin/stdout or context.
	if err := cmd.Start(); err != nil {
		return r.Ref, fmt.Errorf("helper start failed; launch tombstone retained: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	_ = rd.Close()
	message := launchMessage{Spec: spec, Auth: r.Auth, Executable: executable, Env: effectiveEnv, HistoryBytes: b.history}
	if err := json.NewEncoder(wr).Encode(message); err != nil {
		return r.Ref, fmt.Errorf("helper launch acknowledgement uncertain: %w", err)
	}
	_ = wr.Close()
	for {
		latest, e := b.readReceipt(r.Ref.ExecutionID)
		if e != nil {
			return r.Ref, e
		}
		r = latest
		if r.Stage == "running" {
			o, e := b.observe(ctx, r)
			if e == nil && o.Alive {
				return o.Ref, nil
			}
			if e != nil && ctx.Err() != nil {
				return r.Ref, e
			}
		}
		if r.Stage == "exited" || r.Stage == "failed" || r.Stage == "stopped" {
			return r.Ref, fmt.Errorf("native process unavailable: %s", r.Detail)
		}
		select {
		case <-ctx.Done():
			return r.Ref, fmt.Errorf("native launch acknowledgement uncertain; reconcile exact execution: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func (b *Backend) Lookup(ctx context.Context, execution, operation domain.ID) (bpruntime.Observation, error) {
	if !validID(execution) || !validID(operation) {
		return bpruntime.Observation{}, errors.New("lookup requires exact execution and operation")
	}
	r, err := b.readReceipt(execution)
	if os.IsNotExist(err) {
		return bpruntime.Observation{Detail: "No native launch receipt; absence does not authorize replay."}, ErrUnavailable
	}
	if err != nil {
		return bpruntime.Observation{}, err
	}
	if r.Ref.OperationID != operation {
		return bpruntime.Observation{}, conflict("operation does not match execution")
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
func (b *Backend) observe(ctx context.Context, r receipt) (bpruntime.Observation, error) {
	o := bpruntime.Observation{Ref: r.Ref}
	boot, err := bootID()
	if err != nil {
		return o, err
	}
	if boot != r.Ref.BootID {
		o.Detail = "Host boot changed; old execution cannot survive. No automatic replay."
		return o, nil
	}
	if r.Stage == "exited" || r.Stage == "failed" || r.Stage == "stopped" {
		o.Detail = "Native process " + r.Stage + "; " + r.Detail + ". Launch tombstone retained; terminal history unavailable."
		return o, nil
	}
	if r.SupervisorPID == 0 {
		o.Detail = "Launch intent has no confirmed supervisor; outcome uncertain. No automatic replay."
		return o, ErrUnavailable
	}
	conn, err := b.connect(ctx, r)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			o.Detail = "Native supervisor missing; process outcome and terminal history unavailable. No automatic replay."
			return o, ErrUnavailable
		}
		return o, err
	}
	defer conn.Close()
	if err := writeMessage(conn, request{Op: "inspect", Auth: r.Auth, Ref: r.Ref}); err != nil {
		return o, err
	}
	var reply response
	if err := readMessage(conn, &reply); err != nil {
		return o, err
	}
	if reply.Error != "" {
		return o, conflict(reply.Error)
	}
	if reply.Ref != r.Ref {
		return o, conflict("supervisor returned a different execution")
	}
	o.Alive = reply.Alive
	o.WritableClients = reply.WritableClients
	o.Detail = reply.Detail
	return o, nil
}
func (b *Backend) Stop(ctx context.Context, ref domain.RuntimeRef) error {
	r, err := b.receiptForRef(ref)
	if err != nil {
		return err
	}
	if r.Stage == "exited" || r.Stage == "failed" || r.Stage == "stopped" {
		return nil
	}
	conn, err := b.connect(ctx, r)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := writeMessage(conn, request{Op: "stop", Auth: r.Auth, Ref: r.Ref}); err != nil {
		return err
	}
	var reply response
	if err := readMessage(conn, &reply); err != nil {
		return fmt.Errorf("stop acknowledgement uncertain: %w", err)
	}
	if reply.Error != "" {
		return conflict(reply.Error)
	}
	return nil
}
func (b *Backend) AttachCommand(ref domain.RuntimeRef, readOnly bool) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	r, err := b.receiptForRef(ref)
	if err != nil {
		return nil, err
	}
	if r.Mode != "pty" {
		return nil, errors.New("foreground services do not offer a terminal")
	}
	o, err := b.observe(ctx, r)
	if err != nil {
		return nil, err
	}
	if !o.Alive {
		return nil, conflict(o.Detail)
	}
	helper, err := exec.LookPath(b.helper)
	if err != nil {
		return nil, err
	}
	args := []string{helper, "attach", "--state", b.state, "--execution", string(ref.ExecutionID), "--operation", string(ref.OperationID), "--instance", ref.Session}
	if readOnly {
		args = append(args, "--read-only")
	}
	return args, nil
}
func (b *Backend) ValidateLink(ctx context.Context, session, execution, operation domain.ID, token string) error {
	if !validID(session) || !validID(execution) || !validID(operation) || !identityPattern.MatchString(token) {
		return conflict("managed link requires exact inherited launch context")
	}
	r, err := b.readReceipt(execution)
	if err != nil {
		return err
	}
	if r.SessionID != session || r.Ref.OperationID != operation || subtle.ConstantTimeCompare([]byte(token), []byte(r.LinkToken)) != 1 {
		return conflict("managed link authority does not match")
	}
	o, err := b.observe(ctx, r)
	if err != nil {
		return err
	}
	if !o.Alive {
		return conflict(o.Detail)
	}
	return nil
}
func (b *Backend) receiptForRef(ref domain.RuntimeRef) (receipt, error) {
	if ref.Backend != "native" || !validID(ref.ExecutionID) || ref.Socket != b.socket(ref.ExecutionID) {
		return receipt{}, conflict("runtime reference is not this native backend")
	}
	r, err := b.readReceipt(ref.ExecutionID)
	if err != nil {
		return r, err
	}
	if r.Ref != ref {
		return r, conflict("runtime reference differs from durable instance identity")
	}
	return r, nil
}
func (b *Backend) socket(id domain.ID) string {
	return filepath.Join(b.state, executionKey(id)+".sock")
}
func (b *Backend) receiptPath(id domain.ID) string {
	return filepath.Join(b.state, executionKey(id)+".json")
}
func (b *Backend) readReceipt(id domain.ID) (receipt, error) {
	var r receipt
	if !validID(id) {
		return r, errors.New("invalid execution ID")
	}
	data, err := readPrivate(b.receiptPath(id))
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.Version != 1 || r.Ref.Backend != "native" || r.Ref.ExecutionID != id || r.Ref.Socket != b.socket(id) || !validID(r.Ref.OperationID) || !identityPattern.MatchString(r.Auth) || !identityPattern.MatchString(r.LinkToken) || !identityPattern.MatchString(r.Ref.Session) || (r.Mode != "pty" && r.Mode != "process") {
		return r, errors.New("invalid private native receipt")
	}
	return r, nil
}
func (b *Backend) writeReceipt(r receipt) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicWrite(b.receiptPath(r.Ref.ExecutionID), data)
}
func validID(id domain.ID) bool { return identityPattern.MatchString(string(id)) }
func conflict(s string) error   { return domain.ConflictError{Reason: s} }
func executionKey(id domain.ID) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:24]
}
func nonce() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func launchDigest(spec domain.LaunchSpec, mode string) string {
	value := struct {
		Session, Host, Execution, Operation domain.ID
		Generation                          uint64
		Workspace                           string
		Argv, Env                           []string
		Mode                                string
	}{spec.Session.ID, spec.Session.HostID, spec.Session.Execution.ID, spec.OperationID, spec.Session.Execution.Generation, spec.Session.Workspace, spec.Argv, spec.Env, mode}
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func validateSpec(spec domain.LaunchSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if !validID(spec.Session.ID) || !validID(spec.Session.HostID) || !validID(spec.Session.Execution.ID) || !validID(spec.OperationID) {
		return errors.New("invalid launch identity")
	}
	if filepath.Clean(spec.Session.Workspace) != spec.Session.Workspace || strings.ContainsRune(spec.Session.Workspace, 0) {
		return errors.New("invalid workspace")
	}
	for _, arg := range spec.Argv {
		if strings.ContainsRune(arg, 0) {
			return errors.New("NUL in launch argument")
		}
	}
	for _, v := range spec.Env {
		k, value, ok := strings.Cut(v, "=")
		if !ok || !envPattern.MatchString(k) || strings.ContainsRune(value, 0) {
			return errors.New("invalid environment")
		}
		switch k {
		case "BP_SESSION_ID", "BP_EXECUTION_ID", "BP_OPERATION_ID", "BP_HOST_ID", "BP_LINK_TOKEN":
			return errors.New("managed launch identity environment is reserved")
		}
	}
	data, _ := json.Marshal(spec)
	if len(data) > 1<<20 {
		return errors.New("launch specification exceeds 1 MiB")
	}
	return nil
}
func (b *Backend) managedEnv(spec domain.LaunchSpec, token string) []string {
	values := map[string]string{}
	order := []string{}
	add := func(item string) {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			return
		}
		if _, ok := values[key]; !ok {
			order = append(order, key)
		}
		values[key] = value
	}
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(key, "BP_") || key == "TMUX" || key == "TMUX_PANE" {
			continue
		}
		add(v)
	}
	for _, v := range spec.Env {
		add(v)
	}
	if _, ok := values["TERM"]; !ok {
		add("TERM=xterm-256color")
	}
	if _, ok := values["BP_STATE_DIR"]; !ok {
		add("BP_STATE_DIR=" + filepath.Dir(b.state))
	}
	add("BP_SESSION_ID=" + string(spec.Session.ID))
	add("BP_EXECUTION_ID=" + string(spec.Session.Execution.ID))
	add("BP_OPERATION_ID=" + string(spec.OperationID))
	add("BP_HOST_ID=" + string(spec.Session.HostID))
	add("BP_LINK_TOKEN=" + token)
	env := make([]string, 0, len(order))
	for _, key := range order {
		env = append(env, key+"="+values[key])
	}
	return env
}

func environmentDigest(env []string) string {
	data, _ := json.Marshal(env)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
