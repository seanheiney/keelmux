package nativepty

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/seanheiney/keelmux/internal/domain"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Only this compiled test executable recognizes these modes. Installed helper
// and production Backend have no fixture flag or identity-verifier override.
func runStdioTestMode() bool {
	if len(os.Args) < 2 {
		return false
	}
	switch os.Args[1] {
	case "serve":
		fs := flag.NewFlagSet("fixture helper", flag.ContinueOnError)
		state := fs.String("state", "", "")
		execution := fs.String("execution", "", "")
		if fs.Parse(os.Args[2:]) != nil {
			os.Exit(2)
		}
		stdioImageAvailable = func() bool { return true }
		stdioImageVerifier = verifyStdioTestImage
		if err := Serve(*state, domain.ID(*execution), os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	case "app-server":
		os.Exit(runStdioProtocolFixture())
	}
	return false
}
func verifyStdioTestImage(c ps.Channel) error {
	self, err := os.Executable()
	if err != nil || c.Spec.Binary != self {
		return ps.ErrUnavailable
	}
	hash, err := stdioFileDigest(self)
	if err != nil || hash != c.Spec.BinarySHA256 {
		return ps.ErrUnavailable
	}
	start, err := processStart(c.Ref.Runtime.PID)
	if err != nil || start != c.Ref.Runtime.ProcessStart {
		return ps.ErrUnavailable
	}
	return nil
}

type stdioFixtureConfig struct{ Mode, Thread, Workspace string }

func runStdioProtocolFixture() int {
	home := os.Getenv("HOME")
	raw, err := os.ReadFile(filepath.Join(home, "fixture.json"))
	if err != nil {
		return 2
	}
	var config stdioFixtureConfig
	if json.Unmarshal(raw, &config) != nil {
		return 2
	}
	env, _ := json.Marshal(os.Environ())
	if os.WriteFile(filepath.Join(home, "environment.json"), env, 0600) != nil {
		return 2
	}
	log, err := os.OpenFile(filepath.Join(home, "methods.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 2
	}
	defer log.Close()
	var mu sync.Mutex
	emit := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := json.Marshal(v)
		b = append(b, '\n')
		for len(b) > 0 {
			n := len(b)
			if n > 7 {
				n = 7
			}
			_, _ = os.Stdout.Write(b[:n])
			b = b[n:]
		}
	}
	thread := func() map[string]any {
		return map[string]any{"id": config.Thread, "cwd": config.Workspace, "ephemeral": false, "status": map[string]any{"type": "idle"}, "cliVersion": "0.159.0", "createdAt": 1, "updatedAt": 1, "modelProvider": "fixture", "preview": "", "projectId": nil, "sessionId": "fixture-session", "source": "cli", "turns": []any{}}
	}
	callback := func() {
		emit(map[string]any{"id": int64(77), "method": "currentTime/read", "params": map[string]any{}})
		_ = os.WriteFile(filepath.Join(home, "callback-emitted"), []byte("yes"), 0600)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			if _, e := os.Stat(filepath.Join(home, "exit")); e == nil {
				os.Exit(0)
			}
			if _, e := os.Stat(filepath.Join(home, "callback")); e == nil {
				_ = os.Remove(filepath.Join(home, "callback"))
				callback()
			}
			if _, e := os.Stat(filepath.Join(home, "remote-connected")); e == nil {
				_ = os.Remove(filepath.Join(home, "remote-connected"))
				emit(map[string]any{"method": "remoteControl/status/changed", "params": map[string]any{"status": "connected", "serverName": "fixture", "installationId": "fixture"}})
				_ = os.WriteFile(filepath.Join(home, "remote-emitted"), []byte("yes"), 0600)
			}
		}
	}()
	// Protocol-looking stderr is never a native result or receive event.
	_, _ = fmt.Fprintln(os.Stderr, `{"id":"stderr-only","result":{"secret":"fixture-private"}}`)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), ps.MaxFrameBytes)
	for scanner.Scan() {
		var request struct {
			ID     string         `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return 3
		}
		b, _ := json.Marshal(request.Method)
		_, _ = log.Write(append(b, '\n'))
		_ = log.Sync()
		var result any
		switch request.Method {
		case "initialize":
			if config.Mode == "pause_init" {
				_ = os.WriteFile(filepath.Join(home, "init-received"), []byte("yes"), 0600)
				deadline := time.Now().Add(3 * time.Second)
				for {
					if _, e := os.Stat(filepath.Join(home, "release-init")); e == nil {
						break
					}
					if time.Now().After(deadline) {
						return 4
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			if config.Mode == "callback_init" {
				callback()
			}
			if config.Mode == "malformed" {
				mu.Lock()
				_, _ = fmt.Fprintln(os.Stdout, `{"id":"duplicate","id":"duplicate","result":{}}`)
				mu.Unlock()
				continue
			}
			if config.Mode == "unknown_id" {
				emit(map[string]any{"id": "not-issued", "result": nil})
				continue
			}
			if config.Mode == "oversize" {
				mu.Lock()
				_, _ = os.Stdout.Write(make([]byte, ps.MaxFrameBytes+1))
				_, _ = os.Stdout.Write([]byte{'\n'})
				mu.Unlock()
				continue
			}
			if config.Mode == "partial" {
				mu.Lock()
				_, _ = fmt.Fprint(os.Stdout, `{"id":`)
				mu.Unlock()
				return 0
			}
			if config.Mode == "flood" {
				for i := 0; i < ps.MaxEvents+1; i++ {
					callback()
				}
				continue
			}
			result = map[string]any{"userAgent": "fixture", "codexHome": os.Getenv("CODEX_HOME"), "platformFamily": "unix", "platformOs": "fixture"}
		case "initialized":
			continue
		case "thread/resume":
			result = map[string]any{"thread": thread(), "cwd": config.Workspace, "model": "fixture-model", "modelProvider": "fixture", "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandbox": map[string]any{"type": "readOnly"}}
		case "thread/loaded/list":
			result = map[string]any{"data": []string{config.Thread}, "nextCursor": nil}
			if config.Mode == "pagination" {
				result = map[string]any{"data": []string{config.Thread}, "nextCursor": "more"}
			}
		case "thread/read":
			th := thread()
			if config.Mode == "busy" {
				th["status"] = map[string]any{"type": "active", "activeFlags": []string{"waitingOnUserInput"}}
			}
			result = map[string]any{"thread": th}
		case "thread/queue/list", "thread/backgroundTerminals/list":
			result = map[string]any{"data": []any{}, "nextCursor": nil}
		default:
			return 5 // Any unintended native operation is a fixture failure.
		}
		emit(map[string]any{"id": request.ID, "result": result})
	}
	return 0
}
