package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func ircdCLIClient(t *testing.T, handler http.HandlerFunc) *protocol.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &protocol.Client{HTTP: server.Client(), BaseURL: server.URL}
}

func TestIRCDInvalidArgumentsNeverContactDaemon(t *testing.T) {
	var requests atomic.Int32
	c := ircdCLIClient(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) })
	for _, args := range [][]string{
		{}, {"status", "extra"}, {"init", "--irc-port", "-1"}, {"init", "--irc-port", "65536"}, {"init", "--irc-port", "6697", "--api-port", "6697"},
		{"start", "--operation", "operation_unsafe"}, {"start", "positional"}, {"stop"}, {"stop", "--instance", "execution_old"}, {"restart"}, {"restart", "--instance", "execution_old"},
		{"account", "Alice"}, {"account", "alice", "extra"}, {"bind", "--session", "session_bad", "--allow-user", "alice"}, {"bind", "--session", string(domain.NewID("session"))},
		{"bind", "--session", string(domain.NewID("session")), "--allow-user", "alice", "--allow-user", "alice"}, {"bind", "--session", string(domain.NewID("session")), "--allow-user", "Alice"},
	} {
		if err := manageIRCD(context.Background(), c, args); err == nil {
			t.Fatal("invalid IRC arguments accepted", args)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid arguments contacted daemon")
	}
}

func TestIRCDUnknownLifecyclePreservesOperationAndNeverAutomaticallyRetries(t *testing.T) {
	var requests atomic.Int32
	op := domain.NewID("operation")
	c := ircdCLIClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/ircd/start" || r.Method != "POST" || r.Header.Get("X-Backplane-Protocol") != protocol.Version {
			t.Error("wrong private lifecycle request")
		}
		var in protocol.IRCDStartRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.OperationID != op {
			t.Error("CLI changed explicit original operation")
		}
		w.WriteHeader(409)
		json.NewEncoder(w).Encode(protocol.Error{Message: "managed IRC outcome uncertain"})
	})
	err := manageIRCD(context.Background(), c, []string{"start", "--operation", string(op)})
	if err == nil || !strings.Contains(err.Error(), string(op)) || !strings.Contains(err.Error(), "preserve this same --operation") {
		t.Fatal("unknown launch lost retry identity", err)
	}
	if requests.Load() != 1 {
		t.Fatal("CLI replayed uncertain launch")
	}
}

func TestIRCDExplicitHumanAccountUsesOwnedReferenceWithoutSecretBytes(t *testing.T) {
	dir := t.TempDir()
	var requests atomic.Int32
	c := ircdCLIClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var in protocol.IRCDAccountRequest
		if r.URL.Path != "/v1/ircd/account" || json.NewDecoder(r.Body).Decode(&in) != nil || in.Account != "alice" {
			t.Error("wrong exact services account")
		}
		json.NewEncoder(w).Encode(protocol.IRCDConnection{ServerID: domain.NewID("ircd"), Endpoint: "ircs://127.0.0.1:6697", RealmID: "127.0.0.1:6697", Account: in.Account, CAFile: filepath.Join(dir, "ca.pem"), CredentialRef: filepath.Join(dir, "alice.json"), Version: ircd.Version, Source: ircd.SourceCommit, Target: "darwin/arm64"})
	})
	if err := manageIRCD(context.Background(), c, []string{"account", "alice"}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("account command repeated request")
	}
}

func TestIRCDStopAndRestartPreserveExactExpectedInstance(t *testing.T) {
	instance, operation := domain.NewID("execution"), domain.NewID("operation")
	var requests atomic.Int32
	c := ircdCLIClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/v1/ircd/stop" {
			var in protocol.IRCDStopRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Instance != instance {
				t.Error("stop lost exact instance")
			}
		}
		if r.URL.Path == "/v1/ircd/restart" {
			var in protocol.IRCDRestartRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Instance != instance || in.OperationID != operation {
				t.Error("restart changed expected instance or operation")
			}
		}
		w.WriteHeader(409)
		json.NewEncoder(w).Encode(protocol.Error{Message: "stale owned instance"})
	})
	if err := manageIRCD(context.Background(), c, []string{"stop", "--instance", string(instance)}); err == nil {
		t.Fatal("stale stop reported success")
	}
	if err := manageIRCD(context.Background(), c, []string{"restart", "--instance", string(instance), "--operation", string(operation)}); err == nil {
		t.Fatal("stale restart reported success")
	}
	if requests.Load() != 2 {
		t.Fatal("CLI replayed stale lifecycle mutation")
	}
}

func TestIRCDStatusReadOnlyAndHelpTruthful(t *testing.T) {
	var requests atomic.Int32
	c := ircdCLIClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/ircd/status" {
			t.Error("status used mutation route")
		}
		json.NewEncoder(w).Encode(protocol.IRCDStatus{ServerID: domain.NewID("ircd"), Endpoint: "ircs://127.0.0.1:6697", RealmID: "127.0.0.1:6697", Account: "keelmux-bot", Source: ircd.SourceCommit, Target: "darwin/arm64", State: "initialized", ObservedAt: time.Now().UTC()})
	})
	if err := manageIRCD(context.Background(), c, []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || !strings.Contains(ircdHelp, "manual repair") || !strings.Contains(ircdHelp, "semantic dispatch remains unavailable") {
		t.Fatal("status/help claims exceed owned runtime support")
	}
}
