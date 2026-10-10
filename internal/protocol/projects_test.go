package protocol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircdchannel"
)

func testProject() domain.Project {
	return domain.Project{ID: domain.NewID("project"), HostID: domain.NewID("host"), Name: "Release", Workspace: "/workspace", CreatedAt: time.Unix(100, 0).UTC()}
}
func projectResponseClient(t *testing.T, raw string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(raw)) }))
	t.Cleanup(server.Close)
	return &Client{HTTP: server.Client(), BaseURL: server.URL}
}
func TestProjectClientRejectsAmbiguousOrForeignIdentity(t *testing.T) {
	p := testProject()
	raw, _ := json.Marshal(p)
	good := string(raw)
	for name, body := range map[string]string{
		"duplicate":     strings.Replace(good, `"name":"Release"`, `"name":"Other","name":"Release"`, 1),
		"case-alias":    strings.Replace(good, `"name"`, `"Name"`, 1),
		"unknown":       good[:len(good)-1] + `,"authority":true}`,
		"trailing":      good + ` {}`,
		"foreign-id":    strings.Replace(good, string(p.ID), string(domain.NewID("project")), 1),
		"missing-proof": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := projectResponseClient(t, body)
			if out, err := c.Project(context.Background(), p.ID); err == nil || out != (domain.Project{}) {
				t.Fatal("invalid authority published", out, err)
			}
		})
	}
}
func TestProjectDirectoryBoundAndLongWorkspace(t *testing.T) {
	p := testProject()
	p.Workspace = "/" + strings.Repeat("a", 4095)
	all := make([]domain.Project, 128)
	for i := range all {
		all[i] = p
		all[i].ID = domain.NewID("project")
	}
	raw, _ := json.Marshal(all)
	c := projectResponseClient(t, string(raw))
	if got, err := c.Projects(context.Background()); err != nil || !reflect.DeepEqual(got, all) {
		t.Fatal("bounded directory rejected", len(got), err)
	}
	all = append(all, p)
	raw, _ = json.Marshal(all)
	c = projectResponseClient(t, string(raw))
	if out, err := c.Projects(context.Background()); err == nil || out != nil {
		t.Fatal("oversized directory published")
	}
}
func TestManagedBindResponseMustProveProjectAndExactThreadBot(t *testing.T) {
	p := testProject()
	thread := domain.NewID("thread")
	nick, _ := channels.IRCThreadNick(thread)
	channel, _ := ircdchannel.ProjectChannelName(ircdchannel.Project{ID: p.ID, HostID: p.HostID, Name: p.Name, Workspace: p.Workspace})
	in := IRCDBindRequest{ID: domain.NewID("binding"), ProjectID: p.ID, SessionID: domain.NewID("session"), OperationID: domain.NewID("operation"), Allowlist: []string{"alice"}}
	good := ChannelStatus{ID: in.ID, SessionID: in.SessionID, ThreadID: thread, Provider: channels.IRC, Endpoint: "ircs://127.0.0.1:6697", RealmID: "127.0.0.1:6697", SelfID: nick, ChannelID: channel, Allowlist: in.Allowlist, Active: true, ActivationCutoff: time.Now().UTC(), ProjectID: p.ID, IRCAddressed: true}
	for name, mutate := range map[string]func(*ChannelStatus){
		"good":          func(*ChannelStatus) {},
		"project":       func(s *ChannelStatus) { s.ProjectID = domain.NewID("project") },
		"shared-bot":    func(s *ChannelStatus) { s.SelfID = "keelmux-bot" },
		"unaddressed":   func(s *ChannelStatus) { s.IRCAddressed = false },
		"other-channel": func(s *ChannelStatus) { s.ChannelID = "#other" },
		"semantic":      func(s *ChannelStatus) { s.SemanticDispatch = true },
	} {
		t.Run(name, func(t *testing.T) {
			status := good
			mutate(&status)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/projects/show" {
					json.NewEncoder(w).Encode(p)
				} else {
					json.NewEncoder(w).Encode(status)
				}
			}))
			defer server.Close()
			c := &Client{HTTP: server.Client(), BaseURL: server.URL}
			out, err := c.BindIRCD(context.Background(), in)
			if name == "good" {
				if err != nil || !reflect.DeepEqual(out, good) {
					t.Fatal("valid project proof", out, err)
				}
			} else if err == nil || !reflect.DeepEqual(out, ChannelStatus{}) {
				t.Fatal("foreign project route published", out, err)
			}
		})
	}
}
