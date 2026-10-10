package projectservice

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/store"
)

func setup(t *testing.T) (*Service, *store.Store, *protocol.Client) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state", "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := New(db, domain.NewID("host"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	t.Cleanup(server.Close)
	return s, db, &protocol.Client{HTTP: server.Client(), BaseURL: server.URL}
}

func TestProjectCreationHasExactHomeAndIdempotentIntent(t *testing.T) {
	s, _, c := setup(t)
	ctx := context.Background()
	in := protocol.ProjectCreateRequest{ID: domain.NewID("project"), Name: "Release Café", Workspace: t.TempDir()}
	p, err := c.CreateProject(ctx, in)
	if err != nil || p.HostID != s.host {
		t.Fatal("project home", p, err)
	}
	if again, err := c.CreateProject(ctx, in); err != nil || again != p {
		t.Fatal("retry changed original intent", again, err)
	}
	in.Name = "Different name"
	if _, err = c.CreateProject(ctx, in); err == nil {
		t.Fatal("identity adopted a replacement configuration")
	}
	if got, err := c.Project(ctx, p.ID); err != nil || got != p {
		t.Fatal("show changed provenance", got, err)
	}
	if all, err := c.Projects(ctx); err != nil || len(all) != 1 || all[0] != p {
		t.Fatal("directory changed provenance", all, err)
	}
}

func TestProjectRequestAmbiguityIsRejectedBeforeCreation(t *testing.T) {
	s, db, _ := setup(t)
	in := protocol.ProjectCreateRequest{ID: domain.NewID("project"), Name: "Project", Workspace: t.TempDir()}
	raw, _ := json.Marshal(in)
	good := string(raw)
	cases := map[string]string{
		"duplicate":    strings.Replace(good, `"name":"Project"`, `"name":"Other","name":"Project"`, 1),
		"case-alias":   strings.Replace(good, `"name"`, `"Name"`, 1),
		"caller-home":  good[:len(good)-1] + `,"host_id":"host_foreign"}`,
		"trailing":     good + ` {}`,
		"null":         strings.Replace(good, `"name":"Project"`, `"name":null`, 1),
		"missing":      `{"project_id":"` + string(in.ID) + `","name":"Project"}`,
		"not-object":   `[]`,
		"oversized":    strings.Repeat(" ", 32<<10) + good,
		"invalid-utf8": string([]byte{255}),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/projects/create", strings.NewReader(body))
			r.Header.Set("X-Backplane-Protocol", protocol.Version)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatal("ambiguous request accepted", w.Code, w.Body.String())
			}
		})
	}
	if all, err := db.Projects(context.Background()); err != nil || len(all) != 0 {
		t.Fatal("invalid request created durable state", all, err)
	}
}

func TestProjectReadsCannotChangeScopeOrIgnoreMalformedQueries(t *testing.T) {
	s, db, _ := setup(t)
	for _, item := range []struct{ method, path, body, header string }{
		{"GET", "/v1/projects?ignored=yes", "", protocol.Version},
		{"GET", "/v1/projects", "{}", protocol.Version},
		{"GET", "/v1/projects/show?id=project_bad&id=project_bad", "", protocol.Version},
		{"GET", "/v1/projects/show?id=%zz", "", protocol.Version},
		{"GET", "/v1/projects/show?id=project_bad&other=yes", "", protocol.Version},
		{"GET", "/v1/projects", "", "older"},
	} {
		r := httptest.NewRequest(item.method, item.path, strings.NewReader(item.body))
		r.Header.Set("X-Backplane-Protocol", item.header)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("scope ambiguity accepted", item.path, w.Code)
		}
	}
	p, err := db.CreateProject(context.Background(), store.NewProject(domain.NewID("host"), domain.NewID("project"), "Foreign", t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Project(context.Background(), p.ID); err == nil {
		t.Fatal("foreign home exposed as local project")
	}
	if _, err = s.Projects(context.Background()); err == nil {
		t.Fatal("mixed-home directory exposed")
	}
}
