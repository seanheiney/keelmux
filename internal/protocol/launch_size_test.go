package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func sizeFixture() LaunchRequest {
	return LaunchRequest{OperationID: "op_size", Name: "rules", Workspace: "/workspace", Provider: "grok", ProviderVersion: "reviewed", Argv: []string{"grok", "--rules", ""}, Library: &domain.LibrarySnapshot{ProfileID: "default", ProfileRevision: 1, ResolutionDigest: strings.Repeat("a", 64), ApplicationID: "work", ApplicationDigest: strings.Repeat("b", 64), Source: "client-declaration", Loaded: "unknown"}}
}

func TestLaunchEncodedSizeIncludesEscapesSnapshotAndFinalNewline(t *testing.T) {
	in := sizeFixture()
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(in); err != nil {
		t.Fatal(err)
	}
	plainBudget := MaxControlRequestBytes - b.Len()
	in.Argv[2] = strings.Repeat("x", plainBudget)
	if err := ValidateLaunchRequestSize(in); err != nil {
		t.Fatal("exact boundary rejected", err)
	}
	in.Argv[2] += "x"
	if err := ValidateLaunchRequestSize(in); err == nil {
		t.Fatal("one byte above boundary accepted")
	}
	for _, rules := range []string{strings.Repeat("\"", 32<<10), strings.Repeat("<", 11<<10), strings.Repeat("\n", 32<<10)} {
		in.Argv[2] = rules
		if len(rules) >= MaxControlRequestBytes {
			t.Fatal("bad escaping control")
		}
		if err := ValidateLaunchRequestSize(in); err == nil {
			t.Fatal("escaped oversized argument accepted")
		}
	}
}

func TestLaunchSizeRefusedBeforeHTTPRequest(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) > MaxControlRequestBytes {
			t.Error("unbounded launch reached server", err, len(body))
		}
		_, _ = w.Write([]byte(`{"session":{},"operation":{}}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	in := sizeFixture()
	in.Argv[2] = strings.Repeat("<", 11<<10)
	if _, err := c.Launch(context.Background(), in); err == nil {
		t.Fatal("oversized request accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("preflight made a request")
	}
	in.Argv[2] = "small rules"
	if _, err := c.Launch(context.Background(), in); err != nil {
		t.Fatal("ordinary launch rejected", err)
	}
	if calls.Load() != 1 {
		t.Fatal("positive request control did not run")
	}
}
