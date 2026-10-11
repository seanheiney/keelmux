package codexcontroller

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/seanheiney/keelmux/internal/domain"
)

// These are transport/negative-authority fixtures, never provider evidence.
func ownedWireFixture(t *testing.T, mutate func(map[string]any), authorized *atomic.Int64) (Launch, domain.RuntimeRef) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ko-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0600)
	launch := Launch{Endpoint: path, CodexHome: dir, capability: strings.Repeat("c", 64), Request: StartRequest{OperationID: "start", Target: Target{ExecutionID: "execution"}}}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+launch.capability {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		authorized.Add(1)
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		var req map[string]any
		if json.Unmarshal(raw, &req) != nil || req["method"] != "initialize" {
			return
		}
		result := map[string]any{"id": req["id"], "result": map[string]any{"codexHome": dir, "userAgent": "codex_cli_rs/0.159.0"}}
		if mutate != nil {
			mutate(result)
		}
		raw, _ = json.Marshal(result)
		c.Write(ctx, websocket.MessageText, raw)
		c.Read(ctx)
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	ref := ownedCurrentRef(t, os.Getpid())
	ref.ExecutionID = "execution"
	ref.OperationID = "start"
	return launch, ref
}
func TestOwnedHandshakeRequiresCapabilityAndExactPeer(t *testing.T) {
	var authorized atomic.Int64
	l, r := ownedWireFixture(t, nil, &authorized)
	for _, kind := range []string{"capability", "pid", "start", "boot", "home"} {
		t.Run(kind, func(t *testing.T) {
			badL, badR := l, r
			switch kind {
			case "capability":
				badL.capability = strings.Repeat("d", 64)
			case "pid":
				badR.PID++
			case "start":
				badR.ProcessStart = "wrong"
			case "boot":
				badR.BootID = "wrong"
			case "home":
				badL.CodexHome = t.TempDir()
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := NewOwnedUpstream().Probe(ctx, badL, badR); err == nil {
				t.Fatal("invalid ownership proof admitted")
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := NewOwnedUpstream().Probe(ctx, l, r); err != nil {
		t.Fatal("owned handshake failed", err)
	}
	if authorized.Load() != 2 {
		t.Fatal("wrong PID/lifetime reached authenticated protocol", authorized.Load())
	}
}
func TestOwnedHandshakeRejectsUnknownCallbackMalformedAndWrongAck(t *testing.T) {
	for _, kind := range []string{"callback", "wrong-id", "duplicate-key", "home-missing"} {
		t.Run(kind, func(t *testing.T) {
			var n atomic.Int64
			l, r := ownedWireFixture(t, func(result map[string]any) {
				switch kind {
				case "callback":
					delete(result, "result")
					result["method"] = "item/commandExecution/requestApproval"
					result["params"] = map[string]any{}
				case "wrong-id":
					result["id"] = "another"
				case "duplicate-key":
					result["result"] = json.RawMessage(`{"codexHome":"one","codexHome":"two"}`)
				case "home-missing":
					result["result"] = map[string]any{}
				}
			}, &n)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if NewOwnedUpstream().Probe(ctx, l, r) == nil {
				t.Fatal("invalid native acknowledgement accepted")
			}
		})
	}
}
func TestOwnedSocketAliasAndModeValidation(t *testing.T) {
	var n atomic.Int64
	l, _ := ownedWireFixture(t, nil, &n)
	path, identity, err := ownedSocket(l.Endpoint)
	if err != nil || path != l.Endpoint || identity == "" {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(path), "alias.sock")
	if os.Symlink(path, alias) != nil {
		t.Fatal("alias")
	}
	if _, id, e := ownedSocket(alias); e != nil || id == identity {
		t.Fatal("alias not pinned separately")
	}
	os.Chmod(path, 0666)
	if _, _, e := ownedSocket(alias); e == nil {
		t.Fatal("public socket accepted")
	}
}
func TestOwnedAdapterCannotResumeOrEnableLaunch(t *testing.T) {
	u := NewOwnedUpstream()
	if u.CheckPrimaryReady() == nil || u.Capabilities().Resume {
		t.Fatal("unqualified native lifecycle enabled")
	}
	if _, err := u.Resume(context.Background(), Launch{}, domain.RuntimeRef{}); err == nil {
		t.Fatal("resume enabled")
	}
}
