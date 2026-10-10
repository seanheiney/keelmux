package channelservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/store"
)

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failed(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	var conflict domain.ConflictError
	switch {
	case errors.Is(err, channels.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
	case errors.As(err, &conflict):
		status = http.StatusConflict
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status = http.StatusRequestTimeout
	}
	reply(w, status, protocol.Error{Message: err.Error()})
}

// Decode rejects duplicate, differently-cased and unknown object keys before
// the typed decoder. Ambiguous JSON cannot alter a routing/operation identity.
func request(w http.ResponseWriter, r *http.Request, dst any) bool {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil {
		failed(w, channels.ErrInvalid)
		return false
	}
	fields := map[string]bool{}
	switch dst.(type) {
	case *protocol.ChannelBindRequest:
		for _, k := range []string{"binding_id", "session_id", "provider", "endpoint", "channel_id", "root_id", "credential_file", "allow_users"} {
			fields[k] = true
		}
	case *protocol.ChannelIDRequest:
		fields["binding_id"] = true
	case *protocol.ChannelSendRequest:
		for _, k := range []string{"message_id", "binding_id", "text"} {
			fields[k] = true
		}
	case *deliveryRequest:
		fields["message_id"] = true
	default:
		failed(w, channels.ErrInvalid)
		return false
	}
	d := json.NewDecoder(bytes.NewReader(b))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		failed(w, channels.ErrInvalid)
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !fields[key] || seen[key] {
			failed(w, channels.ErrInvalid)
			return false
		}
		seen[key] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			failed(w, channels.ErrInvalid)
			return false
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		failed(w, channels.ErrInvalid)
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		failed(w, channels.ErrInvalid)
		return false
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		failed(w, channels.ErrInvalid)
		return false
	}
	return true
}

type deliveryRequest struct {
	ID domain.ID `json:"message_id"`
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/channels", func(w http.ResponseWriter, r *http.Request) {
		bindings, err := s.db.ChannelBindings(r.Context())
		if err != nil {
			failed(w, err)
			return
		}
		out := make([]protocol.ChannelStatus, 0, len(bindings))
		for _, b := range bindings {
			out = append(out, protocol.ProjectChannel(b))
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("POST /v1/channels/bind", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.ChannelBindRequest
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
	mux.HandleFunc("POST /v1/channels/disable", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.ChannelIDRequest
		if !request(w, r, &in) {
			return
		}
		if err := s.Disable(r.Context(), in.ID); err != nil {
			failed(w, err)
			return
		}
		reply(w, 204, nil)
	})
	mux.HandleFunc("POST /v1/channels/sync", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.ChannelIDRequest
		if !request(w, r, &in) {
			return
		}
		out, err := s.Sync(r.Context(), in.ID)
		if err != nil {
			failed(w, err)
			return
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("POST /v1/channels/send", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.ChannelSendRequest
		if !request(w, r, &in) {
			return
		}
		out, err := s.Send(r.Context(), in)
		if err != nil {
			failed(w, err)
			return
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("POST /v1/channels/reconcile", func(w http.ResponseWriter, r *http.Request) {
		var in deliveryRequest
		if !request(w, r, &in) {
			return
		}
		out, err := s.Reconcile(r.Context(), in.ID)
		if err != nil {
			failed(w, err)
			return
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("GET /v1/channels/delivery", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if len(q) != 1 || len(q["id"]) != 1 || !channels.ValidID(q.Get("id")) {
			failed(w, channels.ErrInvalid)
			return
		}
		out, err := s.db.ChannelDelivery(r.Context(), domain.ID(q.Get("id")))
		if err != nil {
			failed(w, err)
			return
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("GET /v1/channels/history", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if len(q) != 3 || len(q["thread"]) != 1 || len(q["after"]) != 1 || len(q["limit"]) != 1 || !channels.ValidID(q.Get("thread")) {
			failed(w, channels.ErrInvalid)
			return
		}
		after, err := strconv.ParseInt(q.Get("after"), 10, 64)
		limit, le := strconv.Atoi(q.Get("limit"))
		if err != nil || le != nil || after < 0 || limit < 1 || limit > channels.MaxPageSize {
			failed(w, channels.ErrInvalid)
			return
		}
		thread := domain.ID(q.Get("thread"))
		messages, err := s.db.ChannelMessages(r.Context(), thread, after, limit)
		if err != nil {
			failed(w, err)
			return
		}
		reply(w, 200, protocol.ChannelHistory{ThreadID: thread, Messages: messages, SnapshotOnly: true})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backplane-Protocol", protocol.Version)
		if r.Header.Get("X-Backplane-Protocol") != protocol.Version {
			failed(w, channels.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
