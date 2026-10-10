package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/seanheiney/keelmux/internal/protocol"
)

func TestDaemonStopRefusesAmbiguousOrStaleIdentity(t *testing.T) {
	const instance = "daemon_0123456789abcdef0123456789abcdef"
	var stops atomic.Int32
	h := LifecycleHandler(protocol.DaemonStatus{Instance: instance, PID: 123, Protocol: protocol.Version}, func() { stops.Add(1) })
	for _, body := range []string{
		`{}`, `null`, `[]`, `{"instance":""}`, `{"instance":42}`,
		`{"instance":"other"}`, `{"Instance":"` + instance + `"}`,
		`{"instance":"other","instance":"` + instance + `"}`,
		`{"instance":"` + instance + `","instance":"other"}`,
		`{"instance":"` + instance + `","extra":true}`,
		`{"instance":"` + instance + `"} {}`, `{"instance":"` + strings.Repeat("x", 600) + `"}`,
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/daemon/stop", strings.NewReader(body))
		r.Header.Set("X-Backplane-Protocol", protocol.Version)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest && w.Code != http.StatusConflict {
			t.Fatalf("ambiguous/stale request accepted: status=%d body=%q", w.Code, body)
		}
	}
	for _, test := range []struct{ method, path, version string }{
		{http.MethodPost, "/v1/daemon/stop", "bp/2"},
		{http.MethodGet, "/v1/daemon/stop", protocol.Version},
		{http.MethodPost, "/v1/daemon", protocol.Version},
	} {
		r := httptest.NewRequest(test.method, test.path, strings.NewReader(`{"instance":"`+instance+`"}`))
		r.Header.Set("X-Backplane-Protocol", test.version)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code < 400 {
			t.Fatalf("wrong method/protocol accepted: %+v", test)
		}
	}
	if stops.Load() != 0 {
		t.Fatal("refused lifecycle request stopped the current daemon")
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/daemon", nil)
	r.Header.Set("X-Backplane-Protocol", protocol.Version)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), instance) || stops.Load() != 0 {
		t.Fatal("status failed or changed lifecycle authority")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, "/v1/daemon/stop", strings.NewReader(`{"instance":"`+instance+`"}`))
			r.Header.Set("X-Backplane-Protocol", protocol.Version)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusNoContent {
				t.Errorf("exact repeated stop failed: %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if stops.Load() != 1 {
		t.Fatalf("exact repeated stop cancelled %d times", stops.Load())
	}
}
