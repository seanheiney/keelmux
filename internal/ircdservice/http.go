package ircdservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
	"github.com/seanheiney/keelmux/internal/ircdchannel"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/store"
)

func reply(w http.ResponseWriter, status int, out any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if out != nil {
		_ = json.NewEncoder(w).Encode(out)
	}
}
func failed(w http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "managed IRC operation failed"
	var conflict domain.ConflictError
	switch {
	case errors.Is(err, channels.ErrInvalid), errors.Is(err, ircdchannel.ErrScope):
		status, message = http.StatusBadRequest, "invalid managed IRC request"
	case errors.Is(err, store.ErrNotFound), errors.Is(err, ircd.ErrNotInitialized):
		status, message = http.StatusNotFound, "managed IRC state or session was not found"
	case errors.Is(err, ircd.ErrConflict), errors.As(err, &conflict):
		status, message = http.StatusConflict, "managed IRC identity or operation conflicts with original ownership"
	case errors.Is(err, ircd.ErrUncertain), errors.Is(err, ircdchannel.ErrUncertain):
		status, message = http.StatusConflict, "managed IRC outcome uncertain; preserve the original operation and reconcile without replay"
	case errors.Is(err, ircd.ErrRepair):
		status, message = http.StatusConflict, "managed IRC state requires repair; preserve the original journals"
	case errors.Is(err, ircdchannel.ErrAuthentication):
		status, message = http.StatusConflict, "managed IRC authentication was not confirmed; consumed channel intents only permit read-only reconciliation"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, message = http.StatusRequestTimeout, "managed IRC request timed out; preserve the original operation"
	}
	// Arbitrary dependency errors may contain private paths or credentials.
	// The HTTP surface emits only these fixed bounded diagnostic messages.
	reply(w, status, protocol.Error{Message: message})
}

func request(w http.ResponseWriter, r *http.Request, dst any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failed(w, channels.ErrInvalid)
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil || !utf8.Valid(raw) {
		failed(w, channels.ErrInvalid)
		return false
	}
	typeOf := reflect.TypeOf(dst)
	if typeOf == nil || typeOf.Kind() != reflect.Pointer || typeOf.Elem().Kind() != reflect.Struct {
		failed(w, channels.ErrInvalid)
		return false
	}
	fields := map[string]reflect.StructField{}
	for i := 0; i < typeOf.Elem().NumField(); i++ {
		field := typeOf.Elem().Field(i)
		fields[strings.Split(field.Tag.Get("json"), ",")[0]] = field
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		failed(w, channels.ErrInvalid)
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		name, ok := token.(string)
		field, exists := fields[name]
		if err != nil || !ok || !exists || seen[name] {
			failed(w, channels.ErrInvalid)
			return false
		}
		seen[name] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			failed(w, channels.ErrInvalid)
			return false
		}
		if field.Type.Kind() == reflect.Slice {
			var values []string
			if json.Unmarshal(value, &values) != nil || values == nil || len(values) > 256 {
				failed(w, channels.ErrInvalid)
				return false
			}
			for _, v := range values {
				if !channels.ValidID(v) {
					failed(w, channels.ErrInvalid)
					return false
				}
			}
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		failed(w, channels.ErrInvalid)
		return false
	}
	if _, err = d.Token(); err != io.EOF {
		failed(w, channels.ErrInvalid)
		return false
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		failed(w, channels.ErrInvalid)
		return false
	}
	return true
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ircd/init", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.IRCDInitRequest
		if !request(w, r, &in) {
			return
		}
		if in.IRCPort < 0 || in.IRCPort > 65535 || in.APIPort < 0 || in.APIPort > 65535 || in.IRCPort != 0 && in.IRCPort == in.APIPort {
			failed(w, channels.ErrInvalid)
			return
		}
		out, err := s.manager.Init(r.Context(), ircd.InitOptions{IRCPort: in.IRCPort, APIPort: in.APIPort})
		if err != nil {
			failed(w, err)
			return
		}
		reply(w, 200, protocol.ProjectIRCDConnection(out))
	})
	mux.HandleFunc("GET /v1/ircd/status", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
		if err != nil || len(body) != 0 {
			failed(w, channels.ErrInvalid)
			return
		}
		out, err := s.manager.Status(r.Context())
		if err != nil {
			failed(w, err)
			return
		}
		reply(w, 200, protocol.ProjectIRCDStatus(out))
	})
	mux.HandleFunc("POST /v1/ircd/start", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.IRCDStartRequest
		if !request(w, r, &in) {
			return
		}
		if !validOperation(in.OperationID) {
			failed(w, channels.ErrInvalid)
			return
		}
		out, err := s.manager.Start(r.Context(), in.OperationID)
		if err != nil {
			failed(w, err)
			return
		}
		reply(w, 200, protocol.ProjectIRCDStatus(out))
	})
	mux.HandleFunc("POST /v1/ircd/stop", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.IRCDStopRequest
		if !request(w, r, &in) {
			return
		}
		if !validID(in.Instance, "execution") {
			failed(w, channels.ErrInvalid)
			return
		}
		if err := s.manager.Stop(r.Context(), in.Instance); err != nil {
			failed(w, err)
			return
		}
		reply(w, 204, nil)
	})
	mux.HandleFunc("POST /v1/ircd/restart", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.IRCDRestartRequest
		if !request(w, r, &in) {
			return
		}
		if !validID(in.Instance, "execution") || !validOperation(in.OperationID) {
			failed(w, channels.ErrInvalid)
			return
		}
		out, err := s.manager.Restart(r.Context(), in.Instance, in.OperationID)
		if err != nil {
			failed(w, err)
			return
		}
		reply(w, 200, protocol.ProjectIRCDStatus(out))
	})
	mux.HandleFunc("POST /v1/ircd/account", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.IRCDAccountRequest
		if !request(w, r, &in) {
			return
		}
		cfg, err := s.manager.Config()
		if err != nil {
			failed(w, err)
			return
		}
		if in.Account != cfg.Account || in.Account != "keelmux-bot" {
			failed(w, ircd.ErrConflict)
			return
		}
		cred, err := s.manager.EnsureAccount(r.Context(), in.Account)
		if err != nil {
			failed(w, err)
			return
		}
		if cred.Account != cfg.Account || cred.CredentialRef != cfg.CredentialRef {
			failed(w, ircd.ErrConflict)
			return
		}
		reply(w, 200, protocol.ProjectIRCDConnection(cfg))
	})
	mux.HandleFunc("POST /v1/ircd/bind", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.IRCDBindRequest
		if !request(w, r, &in) {
			return
		}
		out, err := s.Bind(r.Context(), in)
		if err != nil {
			failed(w, err)
			return
		}
		reply(w, 200, out)
	})
	methods := map[string]string{"/v1/ircd/init": "POST", "/v1/ircd/status": "GET", "/v1/ircd/start": "POST", "/v1/ircd/stop": "POST", "/v1/ircd/restart": "POST", "/v1/ircd/account": "POST", "/v1/ircd/bind": "POST"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backplane-Protocol", protocol.Version)
		values := r.Header.Values("X-Backplane-Protocol")
		if len(values) != 1 || values[0] != protocol.Version || r.URL.RawQuery != "" {
			failed(w, channels.ErrInvalid)
			return
		}
		method, exists := methods[r.URL.Path]
		if !exists {
			reply(w, 404, protocol.Error{Message: "managed IRC route not found"})
			return
		}
		if r.Method != method {
			reply(w, 405, protocol.Error{Message: "managed IRC method not allowed"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
