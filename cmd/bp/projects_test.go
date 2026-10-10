package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func TestProjectInvalidArgumentsNeverContactDaemon(t *testing.T) {
	var calls atomic.Int32
	c := ircdCLIClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })
	for _, args := range [][]string{{}, {"unknown"}, {"list", "extra"}, {"show"}, {"show", "project_bad"}, {"create"}, {"create", "--name", "Project"}, {"create", "--name", "Project", "--workspace", "/work", "--project", "project_bad"}} {
		if err := manageProjects(context.Background(), c, args); err == nil {
			t.Fatal("invalid project arguments accepted", args)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid CLI requested external operation")
	}
}
func TestProjectLostResponseRetainsOriginalIntentWithoutReplay(t *testing.T) {
	id := domain.NewID("project")
	workspace := t.TempDir()
	var calls atomic.Int32
	c := ircdCLIClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in protocol.ProjectCreateRequest
		if r.URL.Path != "/v1/projects/create" || r.Method != "POST" || json.NewDecoder(r.Body).Decode(&in) != nil || in.ID != id || in.Name != "Launch" || in.Workspace != workspace {
			t.Error("CLI changed original project scope")
		}
		w.WriteHeader(409)
		json.NewEncoder(w).Encode(protocol.Error{Message: "original intent needs reconciliation"})
	})
	err := manageProjects(context.Background(), c, []string{"create", "--project", string(id), "--name", "Launch", "--workspace", workspace})
	if err == nil || !strings.Contains(err.Error(), string(id)) || !strings.Contains(err.Error(), "same --project") || calls.Load() != 1 {
		t.Fatal("unknown result lost intent or replayed", err, calls.Load())
	}
}
