package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/seanheiney/keelmux/internal/protocol"
)

// LifecycleHandler stops only this daemon incarnation. It does not stop a
// native execution, target a saved PID, clear grants or replay an operation.
// The caller serves it on the same owned private socket as the home API.
func LifecycleHandler(status protocol.DaemonStatus, cancel func()) http.Handler {
	var stopped sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backplane-Protocol", protocol.Version)
		if r.Header.Get("X-Backplane-Protocol") != protocol.Version {
			writeJSON(w, http.StatusBadRequest, protocol.Error{Message: "X-Backplane-Protocol must be bp/1"})
			return
		}
		switch {
		case r.URL.Path == "/v1/daemon" && r.Method == http.MethodGet:
			writeJSON(w, http.StatusOK, status)
		case r.URL.Path == "/v1/daemon/stop" && r.Method == http.MethodPost:
			var request protocol.DaemonStopRequest
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512))
			// A duplicate identity must not turn an ambiguous request into a
			// successful stop through the JSON decoder's last-value-wins rule.
			open, err := d.Token()
			valid := err == nil && open == json.Delim('{')
			seen := false
			for valid && d.More() {
				key, err := d.Token()
				if err != nil || key != "instance" || seen || d.Decode(&request.Instance) != nil {
					valid = false
					break
				}
				seen = true
			}
			close, err := d.Token()
			if !valid || !seen || err != nil || close != json.Delim('}') || d.Decode(new(any)) != io.EOF || request.Instance == "" {
				writeJSON(w, http.StatusBadRequest, protocol.Error{Message: "exact daemon instance required"})
				return
			}
			if request.Instance != status.Instance {
				writeJSON(w, http.StatusConflict, protocol.Error{Message: "daemon instance changed; current daemon was preserved"})
				return
			}
			w.WriteHeader(http.StatusNoContent)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			stopped.Do(cancel)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, protocol.Error{Message: "unsupported daemon lifecycle method"})
		}
	})
}
