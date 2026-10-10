package ircdservice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func TestIRCDClientRejectsAmbiguousAndIncompleteStatusBeforePublication(t *testing.T) {
	_, m, _, _, _ := setup(t)
	good, _ := json.Marshal(protocol.ProjectIRCDStatus(m.status))
	body := string(good)
	cases := map[string]string{
		"duplicate":      strings.Replace(body, `"state":"initialized"`, `"state":"running","state":"initialized"`, 1),
		"case-alias":     strings.Replace(body, `"state":"initialized"`, `"State":"initialized"`, 1),
		"null-field":     strings.Replace(body, `"account":"keelmux-bot"`, `"account":null`, 1),
		"trailing":       body + ` {}`,
		"unknown":        body[:len(body)-1] + `,"password":"private"}`,
		"utf8":           strings.Replace(body, "keelmux-bot", string([]byte{255}), 1),
		"oversized":      strings.Replace(body, "keelmux-bot", strings.Repeat("a", 1025), 1),
		"missing-proof":  `{}`,
		"public-realm":   strings.Replace(body, "127.0.0.1", "external.example", -1),
		"invented-state": strings.Replace(body, "initialized", "healthy", 1),
		"wrong-server":   strings.Replace(body, string(m.cfg.ID), string(domain.NewID("session")), 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(raw)) }))
			defer server.Close()
			c := &protocol.Client{HTTP: server.Client(), BaseURL: server.URL}
			out, err := c.IRCD(context.Background())
			if err == nil || !reflect.DeepEqual(out, protocol.IRCDStatus{}) {
				t.Fatal("invalid response published IRC authority")
			}
		})
	}
}

func TestIRCDStartResponseMustConfirmRequestedOperation(t *testing.T) {
	_, m, _, _, _ := setup(t)
	requested := domain.NewID("operation")
	m.Start(context.Background(), domain.NewID("operation"))
	raw, _ := json.Marshal(protocol.ProjectIRCDStatus(m.status))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(raw) }))
	defer server.Close()
	c := &protocol.Client{HTTP: server.Client(), BaseURL: server.URL}
	if out, err := c.StartIRCD(context.Background(), requested); err == nil || !reflect.DeepEqual(out, protocol.IRCDStatus{}) {
		t.Fatal("foreign operation response adopted as requested launch")
	}
}
