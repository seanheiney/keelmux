// codexbrokerprobe is a controlled acceptance helper, never a product launcher.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	cb "github.com/seanheiney/agent-backplane/internal/codexbroker"
	"github.com/seanheiney/agent-backplane/internal/codexbroker/wire"
	"github.com/seanheiney/agent-backplane/internal/daemon"
	"github.com/seanheiney/agent-backplane/internal/domain"
	"github.com/seanheiney/agent-backplane/internal/protocol"
	bpruntime "github.com/seanheiney/agent-backplane/internal/runtime"
	"github.com/seanheiney/agent-backplane/internal/store"
	"golang.org/x/sys/unix"
)

const pinnedDigest = "e89718aa1969bfc4a471277bdc4679a3a3529293de0a309909822dfd67ddb77a"

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func require(ok bool, message string) error {
	if !ok {
		return errors.New(message)
	}
	return nil
}

type process struct {
	cmd         *exec.Cmd
	done        chan struct{}
	start, boot string
	socket      string
	inode       os.FileInfo
}

func boot(ctx context.Context) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("proof supports the reviewed Darwin installation only")
	}
	b, err := exec.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "kern.boottime").Output()
	s := strings.TrimSpace(string(b))
	end := strings.IndexByte(s, '}')
	if err != nil || end < 0 {
		return "", errors.New("live boot metadata unavailable")
	}
	return "darwin:" + s[:end+1], nil
}
func start(ctx context.Context, pid int) (string, error) {
	c := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "stat=")
	c.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	b, err := c.Output()
	f := strings.Fields(string(b))
	if err != nil || len(f) != 6 || strings.Contains(f[5], "Z") {
		return "", errors.New("owned process lifetime unavailable")
	}
	return "darwin-lstart:" + strings.Join(f[:5], " "), nil
}
func (p *process) live(ctx context.Context) error {
	select {
	case <-p.done:
		return errors.New("owned child exited")
	default:
	}
	s, err := start(ctx, p.cmd.Process.Pid)
	if err != nil || s != p.start {
		return errors.New("owned child start differs")
	}
	b, err := boot(ctx)
	if err != nil || b != p.boot {
		return errors.New("owned child boot differs")
	}
	if p.socket != "" {
		info, err := os.Lstat(p.socket)
		if err != nil || !os.SameFile(info, p.inode) {
			return errors.New("owned socket inode differs")
		}
	}
	return nil
}
func (p *process) stop() {
	select {
	case <-p.done:
	default:
		_ = p.cmd.Process.Signal(os.Interrupt)
		select {
		case <-p.done:
		case <-time.After(time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	}
	// Native endpoints can live under a shared per-user daemon parent. Remove
	// only the exact inode from our completed launch; never change the parent.
	if p.socket != "" {
		current, err := os.Lstat(p.socket)
		if err == nil && os.SameFile(current, p.inode) {
			_ = os.Remove(p.socket)
		}
	}
}

type harness struct {
	state, binary, workspace, home string
	env, args                      []string
	primary                        *process
	ref                            domain.RuntimeRef
	mu                             sync.Mutex
	ui                             []*process
	channels                       []*cb.Channel
	admin                          *rpc
	agent                          cb.AgentBinding
	writers                        map[string]bool
	connections                    map[string]registeredConnection
	trace                          *tracedPolicy
	report                         map[string]any
}
type registeredConnection struct {
	binding      cb.ConnectionBinding
	child        *process
	endpoint     string
	inode        os.FileInfo
	peerVerified bool
}

func (h *harness) launch(ctx context.Context, requested string) (*process, error) {
	args := append([]string{"app-server", "--listen", "unix://" + requested}, h.args...)
	args = append([]string{"-c", `umask 077; exec "$@"`, "bp-owned-native", h.binary}, args...)
	c := exec.Command("/bin/sh", args...)
	c.Env, c.Dir = h.env, h.workspace
	c.Stdin, c.Stdout, c.Stderr = nil, io.Discard, io.Discard
	if err := c.Start(); err != nil {
		return nil, errors.New("owned app-server spawn failed")
	}
	p := &process{cmd: c, done: make(chan struct{})}
	go func() { _ = c.Wait(); close(p.done) }()
	var err error
	p.start, err = start(ctx, c.Process.Pid)
	if err != nil {
		p.stop()
		return nil, err
	}
	p.boot, err = boot(ctx)
	if err != nil {
		p.stop()
		return nil, err
	}
	if err := h.childReceipt(p); err != nil {
		p.stop()
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, e := os.Lstat(requested); e == nil {
			actual := requested
			if info.Mode()&os.ModeSymlink != 0 {
				actual, e = filepath.EvalSymlinks(requested)
				if e != nil {
					p.stop()
					return nil, errors.New("owned startup alias unresolved")
				}
			}
			p.socket = actual
			p.inode, err = os.Lstat(actual)
			if err == nil && p.inode.Mode()&os.ModeSocket != 0 {
				// Tie this socket to the spawned app-server, rather than proving
				// only that some same-user process owns a private pathname.
				peerConn, peerErr := net.DialTimeout("unix", actual, time.Second)
				if peerErr != nil {
					p.stop()
					return nil, errors.New("owned upstream peer unavailable")
				}
				peerPID := unixPeerPID(peerConn)
				_ = peerConn.Close()
				if peerPID != p.cmd.Process.Pid {
					p.stop()
					return nil, errors.New("upstream Unix peer PID differs from spawned app-server")
				}
				// Dial verifies exact owned 0700 parent and 0600 socket. This helper
				// resolves only the alias created by its live spawned child.
				c, err := wire.Dial(ctx, actual)
				if err != nil {
					p.stop()
					return nil, err
				}
				_ = c.Close()
				return p, nil
			}
		}
		select {
		case <-p.done:
			return nil, errors.New("owned app-server exited before endpoint")
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	p.stop()
	return nil, errors.New("owned endpoint deadline exceeded")
}

// This helper backend really launches and verifies its child. Its runtime
// locator is explicitly a test app-server, not a tmux pane/product capability.
func (h *harness) Launch(ctx context.Context, spec domain.LaunchSpec) (domain.RuntimeRef, error) {
	if h.primary != nil || spec.Session.Workspace != h.workspace || len(spec.Argv) != 1 || spec.Argv[0] != h.binary {
		return domain.RuntimeRef{}, errors.New("unexpected helper launch")
	}
	p, err := h.launch(ctx, filepath.Join(h.state, "primary.sock"))
	if err != nil {
		return domain.RuntimeRef{}, err
	}
	h.primary = p
	h.ref = domain.RuntimeRef{Backend: "acceptance-owned-app-server", Socket: p.socket, Session: string(spec.Session.ID), Pane: "test-helper-primary", PID: p.cmd.Process.Pid, BootID: p.boot, ProcessStart: p.start, ExecutionID: spec.Session.Execution.ID, OperationID: spec.OperationID}
	return h.ref, nil
}
func (h *harness) Lookup(ctx context.Context, execution, operation domain.ID) (bpruntime.Observation, error) {
	if h.ref.ExecutionID != execution || h.ref.OperationID != operation {
		return bpruntime.Observation{}, errors.New("unexpected launch lookup")
	}
	return h.Inspect(ctx, h.ref)
}
func (h *harness) Inspect(ctx context.Context, ref domain.RuntimeRef) (bpruntime.Observation, error) {
	if ref != h.ref || h.primary == nil {
		return bpruntime.Observation{}, errors.New("unexpected primary reference")
	}
	err := h.primary.live(ctx)
	return bpruntime.Observation{Ref: ref, Alive: err == nil, Detail: "spawned child and live boot/start/socket proof"}, err
}
func (h *harness) Stop(ctx context.Context, ref domain.RuntimeRef) error {
	if ref != h.ref {
		return errors.New("unexpected primary stop")
	}
	h.primary.stop()
	return nil
}
func (h *harness) AttachCommand(domain.RuntimeRef, bool) ([]string, error) {
	return nil, errors.New("helper has no product terminal attach command")
}

func (h *harness) VerifyAgent(ctx context.Context, a cb.AgentBinding) error {
	if a != h.agent || h.primary == nil || a.AppServerPID != h.primary.cmd.Process.Pid || a.AppServerBootID != h.primary.boot || a.AppServerProcessStart != h.primary.start {
		return errors.New("unregistered primary provenance")
	}
	return h.primary.live(ctx)
}
func (h *harness) VerifyWriter(ctx context.Context, r cb.WriterRequest) error {
	if err := h.VerifyAgent(ctx, r.Agent); err != nil {
		return err
	}
	h.mu.Lock()
	ok := h.writers[string(raw(r))]
	h.mu.Unlock()
	return require(ok, "unregistered home writer request")
}
func (h *harness) VerifyConnection(ctx context.Context, c cb.ConnectionBinding) error {
	if err := h.VerifyAgent(ctx, c.Agent); err != nil {
		return err
	}
	h.mu.Lock()
	reg, ok := h.connections[string(c.ID)]
	h.mu.Unlock()
	if !ok || !reflect.DeepEqual(reg.binding, c) {
		return errors.New("unregistered immutable connection")
	}
	if c.Role == cb.TypedObserver {
		liveStart, err := start(ctx, os.Getpid())
		return require(err == nil && c.NativePID == os.Getpid() && c.NativeProcessStart == liveStart, "observer is not live owned helper process")
	}
	if reg.child == nil || !reg.peerVerified || c.NativePID != reg.child.cmd.Process.Pid || c.NativeProcessStart != reg.child.start {
		return errors.New("native launch or peer PID mismatch")
	}
	if err := reg.child.live(ctx); err != nil {
		return err
	}
	info, err := os.Lstat(reg.endpoint)
	if err != nil || !os.SameFile(info, reg.inode) {
		return errors.New("native endpoint incarnation mismatch")
	}
	return nil
}

type rpc struct {
	conn *wire.Conn
	mu   sync.Mutex
	next int
}

func openRPC(ctx context.Context, path string) (*rpc, error) {
	c, err := wire.Dial(ctx, path)
	if err != nil {
		return nil, err
	}
	r := &rpc{conn: c}
	_, err = r.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "bp_owned_zero_turn_admin", "title": "Owned fixture administration", "version": "0.0.0"}, "capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false}})
	if err == nil {
		err = c.Write(ctx, []byte(`{"method":"initialized"}`))
	}
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return r, nil
}
func (r *rpc) call(ctx context.Context, method string, params any) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id := r.next
	if err := r.conn.Write(ctx, raw(map[string]any{"id": id, "method": method, "params": params})); err != nil {
		return nil, err
	}
	for {
		b, err := r.conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		var f map[string]json.RawMessage
		if json.Unmarshal(b, &f) != nil {
			return nil, errors.New("administrative malformed frame")
		}
		if string(f["id"]) != strconv.Itoa(id) {
			continue
		}
		if len(f["error"]) != 0 {
			return nil, errors.New("owned administrative RPC rejected: " + method)
		}
		var result map[string]any
		if json.Unmarshal(f["result"], &result) != nil {
			return nil, errors.New("administrative result object absent")
		}
		return result, nil
	}
}
func emptyPage(m map[string]any) bool {
	a, ok := m["data"].([]any)
	return ok && len(a) == 0 && m["nextCursor"] == nil
}
func (h *harness) InspectQuiescence(ctx context.Context, a cb.AgentBinding) (cb.Quiescence, error) {
	if err := h.VerifyAgent(ctx, a); err != nil {
		return cb.Quiescence{}, err
	}
	t, err := h.admin.call(ctx, "thread/read", map[string]any{"threadId": a.NativeThreadID})
	if err != nil {
		return cb.Quiescence{}, err
	}
	thread, ok := t["thread"].(map[string]any)
	if !ok || thread["id"] != a.NativeThreadID {
		return cb.Quiescence{}, errors.New("observed thread mismatch")
	}
	status, _ := thread["status"].(map[string]any)
	q, err := h.admin.call(ctx, "thread/queue/list", map[string]any{"threadId": a.NativeThreadID, "limit": 100})
	if err != nil {
		return cb.Quiescence{}, err
	}
	bg, err := h.admin.call(ctx, "thread/backgroundTerminals/list", map[string]any{"threadId": a.NativeThreadID})
	if err != nil {
		return cb.Quiescence{}, err
	}
	h.mu.Lock()
	detached := true
	for _, p := range h.ui {
		select {
		case <-p.done:
		default:
			detached = false
		}
	}
	h.mu.Unlock()
	return cb.Quiescence{Agent: a, Idle: status["type"] == "idle", EmptyQueue: emptyPage(q), EmptyBackground: emptyPage(bg), NativeWritersDetached: detached, Source: "owned-primary-exact-thread-read-queue-background", ObservedAt: time.Now()}, nil
}

type probe struct{ h *harness }

func (p probe) Inspect(ctx context.Context, a cb.AgentBinding) (cb.Quiescence, error) {
	return p.h.InspectQuiescence(ctx, a)
}

type tracedPolicy struct {
	cb.Policy
	mu                                                           sync.Mutex
	denied, outputErrors, upstreamErrors, turnWrites, resumeAcks int
	globalSuppressed                                             int
	methods                                                      map[string]int
	resumeByConnection                                           map[string]map[string]bool
	issues                                                       []map[string]any
	privateState                                                 string
}

func (p *tracedPolicy) Decode(b []byte) (cb.Frame, error) {
	f, err := p.Policy.Decode(b)
	if err != nil {
		p.mu.Lock()
		p.issues = append(p.issues, map[string]any{"stage": "decode", "envelope_shape": shape(b)})
		_ = os.WriteFile(filepath.Join(p.privateState, fmt.Sprintf("decode-failure-%d.json", len(p.issues))), b, 0600)
		p.mu.Unlock()
	}
	return f, err
}

func (p *tracedPolicy) Classify(c cb.ConnectionBinding, f cb.Frame, b cb.Baseline) (cb.EffectClass, error) {
	class, err := p.Policy.Classify(c, f, b)
	if err != nil || class == cb.Denied {
		p.mu.Lock()
		p.denied++
		p.issues = append(p.issues, map[string]any{"stage": "classify", "method": publicMethod(f.Method), "payload_shape": shape(f.Payload)})
		p.mu.Unlock()
	}
	return class, err
}
func (p *tracedPolicy) NativeOutput(c cb.OutputContext, f cb.Frame) (cb.Frame, bool, error) {
	out, visible, err := p.Policy.NativeOutput(c, f)
	method := f.Method
	if c.OriginalRequest != nil {
		method = c.OriginalRequest.Method
	}
	if method == "config/read" && f.Kind == cb.Result {
		_ = os.WriteFile(filepath.Join(p.privateState, "native-config-source.json"), f.Payload, 0600)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if f.Kind == cb.RPCError {
		p.upstreamErrors++
	}
	if f.Kind == cb.Notification && method == "remoteControl/status/changed" && !visible && err == nil {
		p.globalSuppressed++
	}
	if err != nil {
		p.outputErrors++
		p.issues = append(p.issues, map[string]any{"stage": "projection", "method": publicMethod(method), "payload_shape": shape(f.Payload)})
	}
	if err == nil && visible && f.Kind == cb.Result && method == "thread/resume" {
		var m struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		_ = json.Unmarshal(out.Payload, &m)
		if m.Thread.ID == c.Recipient.Agent.NativeThreadID {
			key := string(c.UpstreamChannelID) + ":" + string(c.OriginOperationID) + ":" + string(c.OriginalRequest.ID)
			connection := string(c.Recipient.ID)
			if p.resumeByConnection[connection] == nil {
				p.resumeByConnection[connection] = map[string]bool{}
			}
			if !p.resumeByConnection[connection][key] {
				p.resumeByConnection[connection][key] = true
				p.resumeAcks++
			}
		}
	}
	return out, visible, err
}

func publicMethod(method string) string {
	for _, reviewed := range strings.Fields(`initialize initialized account/read collaborationMode/list config/read configRequirements/read hooks/list model/list skills/list thread/goal/get thread/list thread/loaded/list thread/read thread/resume thread/turns/list thread/items/list remoteControl/status/changed thread/status/changed thread/started thread/goal/cleared`) {
		if method == reviewed {
			return method
		}
	}
	return "<unreviewed-method>"
}

func shape(b json.RawMessage) any {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return "invalid"
	}
	return valueShape(v, 0)
}
func valueShape(v any, depth int) any {
	if depth > 5 {
		return "bounded"
	}
	switch x := v.(type) {
	case map[string]any:
		m := map[string]any{}
		for k, v := range x {
			// Arbitrary config maps may use a credential as an ordinary key.
			// Publish only this finite reviewed set of public schema names.
			if !schemaKey(k) {
				m["<unreviewed-key>"] = "redacted"
				continue
			}
			m[k] = valueShape(v, depth+1)
		}
		return m
	case []any:
		if len(x) == 0 {
			return []any{}
		}
		return []any{valueShape(x[0], depth+1)}
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	default:
		return "number"
	}
}

func schemaKey(k string) bool {
	return shapeKeys[k]
}

var shapeKeys = func() map[string]bool {
	keys := map[string]bool{}
	for _, key := range strings.Fields(`jsonrpc method params result code message clientInfo name title version capabilities experimentalApi requestAttestation optOutNotificationMethods refreshToken includeLayers cwd cwds cursor limit includeHidden threadId history path model modelProvider runtimeWorkspaceRoots approvalPolicy approvalsReviewer sandbox permissions baseInstructions developerInstructions personality config features plugins apps web_search excludeTurns initialTurnsPage itemsView turnId sortDirection sortKey modelProviders sourceKinds originators archived useStateDbOnly searchTerm parentThreadId ancestorThreadId forceReload userAgent codexHome platformFamily platformOs thread status type activeFlags id data nextCursor backwardsCursor turns items text input content text_elements phase turn item goal objective tokenBudget tokensUsed timeUsedSeconds createdAt updatedAt recencyAt startedAt completedAt durationMs error environments environmentId extra sessionId forkedFromId preview ephemeral section sectionEnteredAt projectId historyMode reasoningEffort cliVersion source originator gitInfo workingDirectory canAcceptDirectInput activePermissionProfile instructionSources disabledPluginIds collaborationMode model_provider review_model model_context_window model_auto_compact_token_limit model_auto_compact_token_limit_scope approval_policy approvals_reviewer sandbox_mode sandbox_workspace_write forced_chatgpt_workspace_id forced_login_method tools instructions developer_instructions compact_prompt model_reasoning_effort model_reasoning_summary model_verbosity service_tier analytics browser_use computer_use desktop origins layers account requiresOpenaiAuth workspaceRouting planType email usesCodexManagedCredentials requirements description displayName isDefault hidden supportedReasoningEfforts defaultReasoningEffort supportedInputModalities supportsPersonality supportsImageDetail originalName baseModel availabilityMode supportedServiceTiers defaultServiceTier supportedImageDetail supportedCapabilities skill hooks skills warnings errors interface dependencies enabled shortDescription supportsParallelToolCalls supportedServiceTiers defaultReasoningEffort reasoningEffort description sandboxPolicy permissionsProfile`) {
		keys[key] = true
	}
	return keys
}()

type sender struct {
	relay *cb.Relay
	trace *tracedPolicy
}

func (s sender) WriteFrame(ctx context.Context, id domain.ID, b []byte) error {
	f, err := s.trace.Policy.Decode(b)
	if err != nil {
		return err
	}
	s.trace.mu.Lock()
	s.trace.methods[f.Method]++
	if f.Method == "turn/start" {
		s.trace.turnWrites++
	}
	s.trace.mu.Unlock()
	return s.relay.WriteFrame(ctx, id, b)
}

func (h *harness) registerWriter(r cb.WriterRequest) {
	h.mu.Lock()
	h.writers[string(raw(r))] = true
	h.mu.Unlock()
}
func (h *harness) observer(ctx context.Context, a cb.Authority) (cb.ConnectionBinding, error) {
	r := cb.WriterRequest{Agent: h.agent, OperationID: domain.NewID("observe"), PrincipalID: fmt.Sprint(os.Geteuid()), ClientID: "owned-helper-observer", Kind: cb.SemanticWriter, ReadOnly: true}
	h.registerWriter(r)
	g, err := a.Acquire(ctx, r)
	if err != nil {
		return cb.ConnectionBinding{}, err
	}
	s, err := start(ctx, os.Getpid())
	if err != nil {
		return cb.ConnectionBinding{}, err
	}
	c := cb.ConnectionBinding{ID: domain.NewID("observer"), Agent: h.agent, Writer: g.WriterRef, Role: cb.TypedObserver, NativeLaunchID: domain.NewID("helper"), EndpointIncarnation: domain.NewID("observer_endpoint"), NativePID: os.Getpid(), NativeProcessStart: s}
	h.mu.Lock()
	h.connections[string(c.ID)] = registeredConnection{binding: c}
	h.mu.Unlock()
	return a.Bind(ctx, c)
}

func (h *harness) native(ctx context.Context, a cb.Authority, relay *cb.Relay, g cb.Grant, index int) (bool, error) {
	dir := filepath.Join(h.state, fmt.Sprintf("view%d", index))
	if err := os.Mkdir(dir, 0700); err != nil {
		return false, err
	}
	nativeHome := filepath.Join(dir, "codex")
	if err := os.Mkdir(nativeHome, 0700); err != nil {
		return false, err
	}
	if err := writeTrust(nativeHome, h.workspace); err != nil {
		return false, err
	}
	endpoint := filepath.Join(dir, "broker.sock")
	listener, err := wire.Listen(endpoint)
	if err != nil {
		return false, err
	}
	defer listener.Close()
	inode, err := os.Lstat(endpoint)
	if err != nil {
		return false, err
	}
	observed := &uiTrace{policy: h.trace.Policy, thread: h.agent.NativeThreadID, requests: map[string]string{}}
	listener = &observedListener{Listener: listener, trace: observed}
	args := []string{"--remote", "unix://" + endpoint, "--disable", "plugins", "--disable", "apps", "-c", "check_for_update_on_startup=false", "resume", h.agent.NativeThreadID, "--no-alt-screen", "-C", h.workspace}
	cmd := exec.Command(h.binary, args...)
	cmd.Dir = h.workspace
	cmd.Env = append([]string(nil), h.env...)
	for i, v := range cmd.Env {
		if strings.HasPrefix(v, "CODEX_HOME=") {
			cmd.Env[i] = "CODEX_HOME=" + nativeHome
		}
	}
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 38, Cols: 120})
	if err != nil {
		return false, errors.New("owned native PTY spawn failed")
	}
	defer terminal.Close()
	p := &process{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	defer p.stop()
	p.start, err = start(ctx, cmd.Process.Pid)
	if err != nil {
		return false, err
	}
	p.boot, err = boot(ctx)
	if err != nil {
		return false, err
	}
	if err := h.childReceipt(p); err != nil {
		return false, err
	}
	h.mu.Lock()
	h.ui = append(h.ui, p)
	h.mu.Unlock()
	c := cb.ConnectionBinding{ID: domain.NewID("native"), Agent: h.agent, Writer: g.WriterRef, Role: cb.NativeLocal, NativeLaunchID: domain.NewID("launch"), EndpointIncarnation: domain.NewID("endpoint"), NativePID: cmd.Process.Pid, NativeProcessStart: p.start}
	h.mu.Lock()
	h.connections[string(c.ID)] = registeredConnection{binding: c, child: p, endpoint: endpoint, inode: inode}
	h.mu.Unlock()
	nativeDone := make(chan error, 1)
	var upstreamFD string
	server := &http.Server{ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
		return context.WithValue(ctx, peerPIDKey{}, unixPeerPID(conn))
	}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, _ := r.Context().Value(peerPIDKey{}).(int)
		if peer != p.cmd.Process.Pid || p.live(ctx) != nil {
			http.Error(w, "owned native peer unavailable", http.StatusForbidden)
			nativeDone <- errors.New("native Unix peer PID did not match spawned handle")
			return
		}
		h.mu.Lock()
		reg := h.connections[string(c.ID)]
		if reg.peerVerified {
			h.mu.Unlock()
			http.Error(w, "owned native endpoint already consumed", http.StatusForbidden)
			return
		}
		reg.peerVerified = true
		h.connections[string(c.ID)] = reg
		h.mu.Unlock()
		beforeFDs, err := unixFDs(ctx)
		if err != nil {
			nativeDone <- err
			return
		}
		upstream, err := wire.Dial(ctx, h.primary.socket)
		if err != nil {
			nativeDone <- err
			return
		}
		afterFDs, err := unixFDs(ctx)
		if err != nil {
			_ = upstream.Close()
			nativeDone <- err
			return
		}
		added := []string{}
		for fd := range afterFDs {
			if !beforeFDs[fd] {
				added = append(added, fd)
			}
		}
		if len(added) != 1 {
			_ = upstream.Close()
			nativeDone <- errors.New("owned upstream descriptor proof ambiguous")
			return
		}
		h.mu.Lock()
		upstreamFD = added[0]
		h.mu.Unlock()
		channel, err := relay.Open(ctx, a, c, upstream)
		if err != nil {
			_ = upstream.Close()
			nativeDone <- err
			return
		}
		h.mu.Lock()
		h.channels = append(h.channels, channel)
		h.mu.Unlock()
		client, err := wire.Accept(w, r)
		if err != nil {
			_ = channel.Close()
			nativeDone <- err
			return
		}
		nativeDone <- channel.RunNative(ctx, client)
		// UI exit ends only RunNative. The home retains this channel/reader
		// until explicit harness cleanup, independently of the disposable UI.
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	var screenMu sync.Mutex
	var screen bytes.Buffer
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		b := make([]byte, 65536)
		for {
			n, err := terminal.Read(b)
			if n > 0 {
				part := b[:n]
				screenMu.Lock()
				_, _ = screen.Write(part)
				screenMu.Unlock()
				if bytes.Contains(part, []byte("\x1b[6n")) {
					_, _ = terminal.Write([]byte("\x1b[1;1R"))
				}
				if bytes.Contains(part, []byte("\x1b[c")) || bytes.Contains(part, []byte("\x1b[0c")) {
					_, _ = terminal.Write([]byte("\x1b[?1;2c"))
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// No Enter, prompt, Ctrl-C or skill input is ever sent. Only terminal status
	// replies above reach stdin. The owned child is stopped after the idle proof.
	deadline := time.Now().Add(10 * time.Second)
	composer := false
	quietReady := false
	var readyAt time.Time
	var relayFailure error
	for time.Now().Before(deadline) {
		screenMu.Lock()
		text := screen.String()
		screenMu.Unlock()
		h.trace.mu.Lock()
		acks, denials, failures := len(h.trace.resumeByConnection[string(c.ID)]), h.trace.denied, h.trace.outputErrors
		h.trace.mu.Unlock()
		composer = strings.Contains(text, "›") || strings.Contains(text, "»")
		observed.mu.Lock()
		delivered := observed.resumeDelivered
		observed.mu.Unlock()
		if composer && acks == 1 && delivered {
			if readyAt.IsZero() {
				readyAt = time.Now()
			}
			if time.Since(readyAt) >= 750*time.Millisecond {
				quietReady = true
				break
			}
		} else {
			readyAt = time.Time{}
		}
		if denials > 0 || failures > 0 {
			break
		}
		select {
		case relayFailure = <-nativeDone:
			deadline = time.Now()
		case <-p.done:
			deadline = time.Now()
		case <-ctx.Done():
			deadline = time.Now()
		default:
		}
		time.Sleep(30 * time.Millisecond)
	}
	p.stop()
	if relayFailure == nil {
		select {
		case <-nativeDone:
		case <-time.After(2 * time.Second):
			return false, errors.New("native UI loop did not detach within deadline")
		}
	}
	_ = terminal.Close()
	select {
	case <-readerDone:
	case <-time.After(time.Second):
	}
	screenMu.Lock()
	privateScreen := append([]byte(nil), screen.Bytes()...)
	screenMu.Unlock()
	_ = os.WriteFile(filepath.Join(dir, "screen.bin"), privateScreen, 0600)
	h.trace.mu.Lock()
	acks := len(h.trace.resumeByConnection[string(c.ID)])
	h.trace.mu.Unlock()
	h.mu.Lock()
	peerVerified := h.connections[string(c.ID)].peerVerified
	h.mu.Unlock()
	observed.mu.Lock()
	delivered, nativeErrors, wireInvalid := observed.resumeDelivered, observed.rpcErrors, observed.invalid
	observed.mu.Unlock()
	h.report[fmt.Sprintf("native_view_%d", index+1)] = map[string]any{"composer": composer, "exact_resume_ack": acks == 1 && delivered, "actual_resume_frame_written": delivered, "post_ready_quiet_interval": quietReady, "native_rpc_errors": nativeErrors, "wire_observer_valid": !wireInvalid, "owned_pid_boot_start": true, "unix_peer_pid_matches_spawned_child": peerVerified, "dedicated_endpoint_inode": true, "terminal_input": "device-status replies only", "screen_bytes": len(privateScreen)}
	h.report[fmt.Sprintf("native_view_%d", index+1)].(map[string]any)["native_ui_loop_detached"] = true
	if relayFailure != nil {
		h.report[fmt.Sprintf("native_view_%d", index+1)].(map[string]any)["relay_failure"] = relayFailure.Error()
	}
	if err := h.primary.live(ctx); err != nil {
		return false, errors.New("native disconnect lost primary")
	}
	fds, err := unixFDs(ctx)
	if err != nil {
		return false, err
	}
	h.mu.Lock()
	fd := upstreamFD
	h.mu.Unlock()
	if fd == "" || !fds[fd] {
		return false, errors.New("dedicated native upstream did not survive UI detach")
	}
	h.report[fmt.Sprintf("native_view_%d", index+1)].(map[string]any)["dedicated_upstream_descriptor_survived_detach"] = true
	if !composer || acks != 1 || !delivered || !quietReady || nativeErrors != 0 || wireInvalid {
		return false, errors.New("native composer or exact projected resume acknowledgement unavailable")
	}
	return true, nil
}

func unixFDs(ctx context.Context) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-n", "-a", "-p", strconv.Itoa(os.Getpid()), "-U", "-Ff").Output()
	if err != nil {
		return nil, errors.New("owned Unix descriptor metadata unavailable")
	}
	fds := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "f") {
			fds[line] = true
		}
	}
	return fds, nil
}

func (h *harness) closeChannels() {
	h.mu.Lock()
	channels := append([]*cb.Channel(nil), h.channels...)
	h.mu.Unlock()
	for _, c := range channels {
		_ = c.Close()
	}
}

type peerPIDKey struct{}

// Observe only bytes actually read/written by the real native socket. This
// independent receipt prevents policy prevalidation from masquerading as
// downstream delivery. It does not modify, authorize or forward any frame.
type uiTrace struct {
	mu                       sync.Mutex
	policy                   cb.Policy
	thread                   string
	requests                 map[string]string
	resumeDelivered, invalid bool
	rpcErrors                int
}

func (u *uiTrace) frame(outgoing bool, b []byte) {
	f, err := u.policy.Decode(b)
	u.mu.Lock()
	defer u.mu.Unlock()
	if err != nil {
		u.invalid = true
		return
	}
	if !outgoing {
		if f.Kind == cb.Request {
			if len(u.requests) >= 4096 {
				u.invalid = true
				return
			}
			u.requests[string(f.ID)] = f.Method
		}
		return
	}
	if f.Kind == cb.RPCError {
		u.rpcErrors++
		return
	}
	if f.Kind == cb.Notification && f.Method == "remoteControl/status/changed" {
		u.invalid = true
		return
	}
	if f.Kind == cb.Result && u.requests[string(f.ID)] == "thread/resume" {
		var value struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if json.Unmarshal(f.Payload, &value) == nil && value.Thread.ID == u.thread {
			u.resumeDelivered = true
		}
	}
}

type observedListener struct {
	net.Listener
	trace *uiTrace
}

func (l *observedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &observedConn{Conn: c, read: wsStream{trace: l.trace}, write: wsStream{trace: l.trace, outgoing: true}}, nil
}

type observedConn struct {
	net.Conn
	read, write wsStream
}

func (c *observedConn) SyscallConn() (syscall.RawConn, error) {
	return c.Conn.(syscall.Conn).SyscallConn()
}
func (c *observedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.read.feed(b[:n])
	}
	return n, err
}
func (c *observedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.write.feed(b[:n])
	}
	if err != nil {
		c.write.fail()
	}
	return n, err
}

type wsStream struct {
	mu                 sync.Mutex
	trace              *uiTrace
	outgoing, upgraded bool
	buffer, message    []byte
	textMessage        bool
}

func (s *wsStream) fail() { s.trace.mu.Lock(); s.trace.invalid = true; s.trace.mu.Unlock() }
func (s *wsStream) feed(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buffer = append(s.buffer, b...)
	if len(s.buffer) > wire.MaxFrameBytes+65536 {
		s.fail()
		s.buffer = nil
		return
	}
	if !s.upgraded {
		end := bytes.Index(s.buffer, []byte("\r\n\r\n"))
		if end < 0 {
			if len(s.buffer) > 16384 {
				s.fail()
			}
			return
		}
		s.buffer = s.buffer[end+4:]
		s.upgraded = true
	}
	for len(s.buffer) >= 2 {
		header := s.buffer
		opcode, fin := header[0]&15, header[0]&128 != 0
		masked := header[1]&128 != 0
		size := uint64(header[1] & 127)
		offset := 2
		if size == 126 {
			if len(header) < 4 {
				return
			}
			size = uint64(binary.BigEndian.Uint16(header[2:4]))
			offset = 4
		} else if size == 127 {
			if len(header) < 10 {
				return
			}
			size = binary.BigEndian.Uint64(header[2:10])
			offset = 10
		}
		if size > wire.MaxFrameBytes {
			s.fail()
			s.buffer = nil
			return
		}
		if masked {
			offset += 4
		}
		if uint64(len(header)) < uint64(offset)+size {
			return
		}
		payload := append([]byte(nil), header[offset:offset+int(size)]...)
		if masked {
			mask := header[offset-4 : offset]
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		s.buffer = s.buffer[offset+int(size):]
		if opcode >= 8 {
			continue
		}
		if opcode == 1 {
			s.message = nil
			s.textMessage = true
		} else if opcode != 0 || !s.textMessage {
			s.fail()
			return
		}
		s.message = append(s.message, payload...)
		if len(s.message) > wire.MaxFrameBytes {
			s.fail()
			return
		}
		if fin {
			s.trace.frame(s.outgoing, s.message)
			s.message = nil
			s.textMessage = false
		}
	}
}

func unixPeerPID(conn net.Conn) int {
	// Darwin SOL_LOCAL=0 and LOCAL_PEERPID=2 are pinned x/sys v0.48.0
	// constants. Numeric options keep the helper buildable on other platforms;
	// run() refuses every platform except the reviewed Darwin installation.
	if runtime.GOOS != "darwin" {
		return 0
	}
	c, ok := conn.(interface {
		SyscallConn() (syscall.RawConn, error)
	})
	if !ok {
		return 0
	}
	rc, err := c.SyscallConn()
	if err != nil {
		return 0
	}
	pid := 0
	err = rc.Control(func(fd uintptr) {
		got, e := unix.GetsockoptInt(int(fd), 0, 2)
		if e == nil {
			pid = got
		}
	})
	if err != nil {
		return 0
	}
	return pid
}

func writeTrust(home, workspace string) error {
	return os.WriteFile(filepath.Join(home, "config.toml"), []byte("[projects."+strconv.Quote(workspace)+"]\ntrust_level = \"trusted\"\n"), 0600)
}

func (h *harness) childReceipt(p *process) error {
	return os.WriteFile(filepath.Join(h.state, fmt.Sprintf("owned-child-%d.json", p.cmd.Process.Pid)), raw(map[string]any{
		"pid": p.cmd.Process.Pid, "parent_pid": os.Getpid(), "boot": p.boot, "start": p.start,
	}), 0600)
}
func (h *harness) setupFixture(ctx context.Context) (string, string, error) {
	p, err := h.launch(ctx, filepath.Join(h.state, "fixture.sock"))
	if err != nil {
		return "", "", err
	}
	defer p.stop()
	r, err := openRPC(ctx, p.socket)
	if err != nil {
		return "", "", err
	}
	defer r.conn.Close()
	ids := []string{}
	for i := 0; i < 2; i++ {
		result, err := r.call(ctx, "thread/start", map[string]any{"model": "gpt-6.1-sol", "modelProvider": "bp_native_proof", "cwd": h.workspace, "sandbox": "read-only", "approvalPolicy": "on-request", "ephemeral": false, "historyMode": "legacy"})
		if err != nil {
			return "", "", err
		}
		thread, ok := result["thread"].(map[string]any)
		id, _ := thread["id"].(string)
		if !ok || id == "" {
			return "", "", errors.New("fixture thread identity unavailable")
		}
		ids = append(ids, id)
		if _, err = r.call(ctx, "thread/name/set", map[string]any{"threadId": id, "name": fmt.Sprintf("Owned zero-turn fixture %d", i+1)}); err != nil {
			return "", "", err
		}
		if _, err = r.call(ctx, "thread/settings/update", map[string]any{"threadId": id, "cwd": h.workspace}); err != nil {
			return "", "", err
		}
	}
	// Observe a durable owned rollout without manufacturing native state. The
	// first primary is closed before registration/grants; final startup must
	// resume the exact on-disk zero-turn thread in a fresh owned process.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		matches, _ := filepath.Glob(filepath.Join(h.home, "sessions", "*", "*", "*", "*"+ids[0]+".jsonl"))
		if len(matches) == 1 {
			h.report["durable_fixture_rollout_observed"] = true
			time.Sleep(250 * time.Millisecond)
			return ids[0], ids[1], nil
		}
		time.Sleep(30 * time.Millisecond)
	}
	return "", "", errors.New("fresh fixture did not produce durable rollout metadata")
}

func (h *harness) run(ctx context.Context) error {
	for _, dir := range []string{"home", "codex", "workspace", "registry"} {
		if err := os.Mkdir(filepath.Join(h.state, dir), 0700); err != nil {
			return err
		}
	}
	h.home = filepath.Join(h.state, "codex")
	h.workspace = filepath.Join(h.state, "workspace")
	h.env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(h.state, "home"), "CODEX_HOME=" + h.home, "TMPDIR=" + h.state, "TERM=xterm-256color"}
	version := exec.CommandContext(ctx, h.binary, "--version")
	version.Env = h.env
	v, err := version.Output()
	if err != nil || strings.TrimSpace(string(v)) != "codex-cli 0.159.0" {
		return errors.New("installed native version differs from pin")
	}
	b, err := os.ReadFile(h.binary)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(b)
	if hex.EncodeToString(digest[:]) != pinnedDigest {
		return errors.New("installed native binary digest differs from reviewed capture")
	}
	h.report["native_version"] = "codex-cli 0.159.0"
	h.report["native_binary_sha256"] = pinnedDigest
	if err := writeTrust(h.home, h.workspace); err != nil {
		return err
	}
	h.args = []string{"--disable", "plugins", "--disable", "apps"}
	for _, override := range []string{"check_for_update_on_startup=false", "analytics.enabled=false", `cli_auth_credentials_store="file"`, `model="gpt-6.1-sol"`, `model_provider="bp_native_proof"`, `model_providers.bp_native_proof.name="Owned zero-turn proof"`, `model_providers.bp_native_proof.base_url="http://127.0.0.1:1/v1"`, `model_providers.bp_native_proof.wire_api="responses"`, "model_providers.bp_native_proof.requires_openai_auth=false", "model_providers.bp_native_proof.supports_websockets=false", `sandbox_mode="read-only"`, `approval_policy="on-request"`} {
		h.args = append(h.args, "-c", override)
	}
	h.report["stage"] = "fixture_before_grant"
	h.report["fixture_history_mode"] = "legacy"
	thread, foreign, err := h.setupFixture(ctx)
	if err != nil {
		return err
	}
	h.report["pregrant_admin_methods"] = []string{"initialize", "initialized", "thread/start", "thread/name/set", "thread/settings/update", "thread/resume", "thread/read", "thread/turns/list", "config/read"}
	st, err := store.Open(filepath.Join(h.state, "registry", "state.sqlite"))
	if err != nil {
		return err
	}
	defer st.Close()
	svc, err := daemon.New(st, h, domain.NewID("host"))
	if err != nil {
		return err
	}
	h.report["stage"] = "registered_primary"
	launched, err := svc.Launch(ctx, protocol.LaunchRequest{OperationID: domain.NewID("launch"), Name: "Owned broker acceptance", Workspace: h.workspace, Provider: "codex", ProviderVersion: "0.159.0", Argv: []string{h.binary}})
	if err != nil {
		return err
	}
	defer h.primary.stop()
	if launched.Operation.Outcome != domain.Completed {
		return errors.New("owned primary launch not durably completed")
	}
	h.admin, err = openRPC(ctx, h.primary.socket)
	if err != nil {
		return err
	}
	defer h.admin.conn.Close()
	resumed, err := h.admin.call(ctx, "thread/resume", map[string]any{"threadId": thread, "excludeTurns": true})
	if err != nil {
		return err
	}
	t, ok := resumed["thread"].(map[string]any)
	if !ok || t["id"] != thread {
		return errors.New("durable exact-thread resume failed")
	}
	h.report["durable_fixture_resume_exact"] = true
	if err := h.verifyZeroHistory(ctx, thread); err != nil {
		return err
	}
	h.report["zero_turn_fixture_verified_before_grant"] = true
	configured, err := h.admin.call(ctx, "config/read", map[string]any{"cwd": h.workspace, "includeLayers": true})
	if err != nil {
		return err
	}
	config, _ := configured["config"].(map[string]any)
	projects, _ := config["projects"].(map[string]any)
	project, _ := projects[h.workspace].(map[string]any)
	if project["trust_level"] != "trusted" {
		return errors.New("authoritative owned workspace trust record unavailable")
	}
	h.report["authoritative_project_trust_record_verified"] = true
	_ = os.WriteFile(filepath.Join(h.state, "pregrant-config-source.json"), raw(configured), 0600)
	vv := launched.Session
	h.agent = cb.AgentBinding{HostID: vv.HostID, SessionID: vv.ID, ExecutionID: vv.Execution.ID, Generation: vv.Execution.Generation, AppServerIncarnation: domain.NewID("appserver"), AppServerPID: h.primary.cmd.Process.Pid, AppServerBootID: h.primary.boot, AppServerProcessStart: h.primary.start, ProtocolVersion: "0.159.0", NativeThreadID: thread}
	forged := h.agent
	forged.AppServerProcessStart = "unowned-start"
	if h.VerifyAgent(ctx, forged) == nil {
		return errors.New("live verifier accepted forged process provenance")
	}
	h.report["forged_primary_provenance_refused"] = true
	h.report["owned_primary_proof"] = map[string]any{"pid": h.primary.cmd.Process.Pid, "boot_and_start_verified_live": true, "spawned_child_handle": true, "socket_inode_verified": true, "socket_mode": "0600", "parent_mode": "0700", "shared_parent_changed": false}
	h.report["owned_primary_proof"].(map[string]any)["unix_peer_pid_matches_spawned_child"] = true
	base := cb.DefaultBaseline(h.workspace)
	h.trace = &tracedPolicy{Policy: cb.NewPolicy(), methods: map[string]int{}, resumeByConnection: map[string]map[string]bool{}, privateState: h.state}
	relay, err := cb.NewRelay(h.trace, base)
	if err != nil {
		return err
	}
	defer h.closeChannels()
	authority := svc.CodexAuthority(h.trace, base, sender{relay, h.trace}, probe{h}, h)
	w := cb.WriterRequest{Agent: h.agent, OperationID: domain.NewID("writer"), PrincipalID: fmt.Sprint(os.Geteuid()), ClientID: "owned-native-proof", Kind: cb.NativeWriter}
	h.registerWriter(w)
	grant, err := authority.Acquire(ctx, w)
	if err != nil {
		return err
	}
	observer, err := h.observer(ctx, authority)
	if err != nil {
		return err
	}
	h.report["real_shared_store_authority"] = true
	for i := 0; i < 2; i++ {
		h.report["stage"] = fmt.Sprintf("native_view_%d", i+1)
		if _, err := h.native(ctx, authority, relay, grant, i); err != nil {
			return err
		}
		h.report["stage"] = "typed_observation"
		observation, err := authority.Observe(ctx, observer)
		if err != nil {
			return err
		}
		if observation.NativeThreadID != thread || observation.SessionID != vv.ID || observation.ExecutionID != vv.Execution.ID || observation.Generation != vv.Execution.Generation || observation.Status != "idle" {
			return errors.New("scoped typed observation differs from idle authoritative thread")
		}
		encoded := string(raw(observation))
		if strings.Contains(encoded, foreign) || strings.Contains(encoded, h.home) || strings.Contains(encoded, "account") || strings.Contains(encoded, "config") {
			return errors.New("semantic observation disclosed private or global metadata")
		}
		h.report[fmt.Sprintf("observation_after_view_%d", i+1)] = map[string]any{"same_native_thread": true, "same_product_generation": true, "status": observation.Status, "private_account_config_absent": true, "other_native_thread_absent": true, "native_disconnect_preserved_primary": true}
	}
	if err := h.verifyZeroHistory(ctx, thread); err != nil {
		return err
	}
	h.report["zero_turns_verified_native_history"] = true
	h.report["zero_history_source"] = "exact legacy thread/read(includeTurns=true) and complete empty descending turns/list"
	h.trace.mu.Lock()
	defer h.trace.mu.Unlock()
	if h.trace.denied != 0 || h.trace.outputErrors != 0 || h.trace.upstreamErrors != 0 || h.trace.turnWrites != 0 {
		return errors.New("native broker policy or RPC error observed")
	}
	h.report["stage"] = "complete"
	return nil
}

func (h *harness) verifyZeroHistory(ctx context.Context, id string) error {
	read, err := h.admin.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": true})
	if err != nil {
		return err
	}
	thread, ok := read["thread"].(map[string]any)
	if !ok || thread["id"] != id {
		return errors.New("zero-turn history target mismatch")
	}
	allTurns, ok := thread["turns"].([]any)
	if !ok || len(allTurns) != 0 {
		return errors.New("legacy full thread history is not empty")
	}
	turns, err := h.admin.call(ctx, "thread/turns/list", map[string]any{"threadId": id, "cursor": nil, "limit": 100, "sortDirection": "desc", "itemsView": "notLoaded"})
	if err != nil {
		return err
	}
	if !emptyPage(turns) {
		return errors.New("zero-turn native history assertion failed")
	}
	return nil
}

func main() {
	native := flag.String("native", "", "exact installed native binary")
	state := flag.String("state", "", "fresh owned private state")
	flag.Parse()
	h := &harness{binary: *native, state: *state, writers: map[string]bool{}, connections: map[string]registeredConnection{}, report: map[string]any{"passed": false, "model_turns_submitted": 0}}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(signalCtx, 60*time.Second)
	defer cancel()
	info, err := os.Lstat(h.state)
	if err == nil && (info.Mode().Perm() != 0700 || !info.IsDir() || !filepath.IsAbs(h.state) || filepath.Clean(h.state) != h.state) {
		err = errors.New("fresh state must be a clean private directory")
	}
	if err == nil {
		err = h.run(ctx)
	}
	if h.trace != nil {
		h.trace.mu.Lock()
		methods := []string{}
		for method := range h.trace.methods {
			methods = append(methods, method)
		}
		sort.Strings(methods)
		h.report["broker_methods_forwarded"] = methods
		h.report["broker_frame_counts"] = h.trace.methods
		h.report["policy_denials"] = h.trace.denied
		h.report["projection_errors"] = h.trace.outputErrors
		h.report["upstream_rpc_errors"] = h.trace.upstreamErrors
		h.report["resume_acknowledgements"] = h.trace.resumeAcks
		h.report["global_remote_control_notifications_suppressed"] = h.trace.globalSuppressed
		h.report["model_turn_writes"] = h.trace.turnWrites
		h.report["safe_shape_gaps"] = h.trace.issues
		h.trace.mu.Unlock()
	}
	if err != nil {
		message := strings.ReplaceAll(err.Error(), h.state, "<STATE>")
		message = strings.ReplaceAll(message, h.binary, "<NATIVE_BINARY>")
		if h.primary != nil {
			message = strings.ReplaceAll(message, h.primary.socket, "<UPSTREAM_SOCKET>")
		}
		h.report["failure"] = message
	} else {
		h.report["passed"] = true
	}
	_ = json.NewEncoder(os.Stdout).Encode(h.report)
	if err != nil {
		os.Exit(1)
	}
}
