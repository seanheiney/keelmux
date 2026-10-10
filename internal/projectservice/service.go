// Package projectservice exposes the explicit host-owned project directory.
// Creating a project performs no discovery, runtime, chat or model activity.
package projectservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/store"
)

var errInvalid = errors.New("invalid project request")

type Service struct {
	db   *store.Store
	host domain.ID
}

func New(db *store.Store, host domain.ID) (*Service, error) {
	if db == nil || !domain.ValidObjectID(host, "host") {
		return nil, errInvalid
	}
	return &Service{db: db, host: host}, nil
}
func (s *Service) Create(ctx context.Context, in protocol.ProjectCreateRequest) (domain.Project, error) {
	p := store.NewProject(s.host, in.ID, in.Name, in.Workspace)
	if p.Validate() != nil {
		return domain.Project{}, errInvalid
	}
	return s.db.CreateProject(ctx, p)
}
func (s *Service) Project(ctx context.Context, id domain.ID) (domain.Project, error) {
	if !domain.ValidObjectID(id, "project") {
		return domain.Project{}, errInvalid
	}
	p, err := s.db.Project(ctx, id)
	if err == nil && p.HostID != s.host {
		return domain.Project{}, domain.ConflictError{Reason: "project belongs to another home"}
	}
	return p, err
}
func (s *Service) Projects(ctx context.Context) ([]domain.Project, error) {
	all, err := s.db.Projects(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range all {
		if p.HostID != s.host {
			return nil, domain.ConflictError{Reason: "project directory belongs to another home"}
		}
	}
	return all, nil
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "project authority unavailable"
	var conflict domain.ConflictError
	switch {
	case errors.Is(err, errInvalid):
		status, message = 400, "invalid project request"
	case errors.Is(err, store.ErrNotFound):
		status, message = 404, "project not found"
	case errors.As(err, &conflict):
		status, message = 409, "project identity or home conflict"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, message = 408, "project request timed out"
	}
	reply(w, status, protocol.Error{Message: message})
}
func decodeRequest(w http.ResponseWriter, r *http.Request, in *protocol.ProjectCreateRequest) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, errInvalid)
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil || !utf8.Valid(raw) {
		failure(w, errInvalid)
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		failure(w, errInvalid)
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || key != "project_id" && key != "name" && key != "workspace" {
			failure(w, errInvalid)
			return false
		}
		seen[key] = true
		var value string
		if d.Decode(&value) != nil {
			failure(w, errInvalid)
			return false
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') || len(seen) != 3 {
		failure(w, errInvalid)
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		failure(w, errInvalid)
		return false
	}
	if json.Unmarshal(raw, in) != nil {
		failure(w, errInvalid)
		return false
	}
	return true
}
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/projects", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			failure(w, errInvalid)
			return
		}
		out, err := s.Projects(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("GET /v1/projects/show", func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q) != 1 || len(q["id"]) != 1 {
			failure(w, errInvalid)
			return
		}
		out, err := s.Project(r.Context(), domain.ID(q.Get("id")))
		if err != nil {
			failure(w, err)
			return
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("POST /v1/projects/create", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			failure(w, errInvalid)
			return
		}
		var in protocol.ProjectCreateRequest
		if !decodeRequest(w, r, &in) {
			return
		}
		out, err := s.Create(r.Context(), in)
		if err != nil {
			failure(w, err)
			return
		}
		reply(w, 200, out)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backplane-Protocol", protocol.Version)
		if r.Header.Get("X-Backplane-Protocol") != protocol.Version {
			failure(w, errInvalid)
			return
		}
		if r.Method == "GET" {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
			if err != nil || len(body) != 0 {
				failure(w, errInvalid)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
