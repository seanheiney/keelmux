package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/protocol"
)

func TestOperationOnlyRetryClientAndHomeRouteUseRecordedOriginal(t *testing.T) {
	e := setup(t)
	first, err := e.service.Launch(context.Background(), e.launchRequest("retry-http"))
	if err != nil {
		t.Fatal(err)
	}
	bodies := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/launch/retry" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			bodies <- string(body)
			r.Body = io.NopCloser(strings.NewReader(string(body)))
		}
		e.service.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	c := &protocol.Client{HTTP: server.Client(), BaseURL: server.URL}
	again, err := c.RetryLaunch(context.Background(), first.Operation.ID)
	if err != nil || again.Session.ID != first.Session.ID || again.Operation.ID != first.Operation.ID {
		t.Fatal("client retry changed original launch", again, err)
	}
	if body := <-bodies; body != "{\"operation_id\":\"retry-http\"}\n" {
		t.Fatal("retry supplied new launch attributes", body)
	}
	if launches, _ := e.backend.counts(); launches != 1 {
		t.Fatal("HTTP retry replayed runtime", launches)
	}
	for _, body := range []string{`{"operation_id":"retry-http","argv":["other"]}`, `{} {}`, strings.Repeat("x", protocol.MaxControlRequestBytes+1)} {
		r := httptest.NewRequest("POST", "/v1/launch/retry", strings.NewReader(body))
		r.Header.Set("X-Backplane-Protocol", protocol.Version)
		w := httptest.NewRecorder()
		e.service.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatal("retry accepted new/invalid request attributes", w.Code)
		}
	}
	_, err = c.RetryLaunch(context.Background(), "missing")
	if errorResponse, ok := err.(*protocol.HTTPError); !ok || errorResponse.StatusCode != http.StatusNotFound {
		t.Fatal("missing original did not refuse", err)
	}
	if launches, _ := e.backend.counts(); launches != 1 {
		t.Fatal("refused retry launched runtime", launches)
	}
}
