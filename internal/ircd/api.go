package ircd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/domain"
)

type apiGeneric struct {
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
}
type authInput struct {
	Account  string `json:"accountName"`
	Password string `json:"passphrase"`
}
type authResponse struct {
	apiGeneric
	Account string `json:"accountName,omitempty"`
}
type accountResponse struct {
	apiGeneric
	Account      string   `json:"accountName,omitempty"`
	Email        string   `json:"email,omitempty"`
	RegisteredAt string   `json:"registeredAt,omitempty"`
	Channels     []string `json:"channels,omitempty"`
}
type apiStatus struct {
	apiGeneric
	Version   string `json:"version"`
	GoVersion string `json:"go_version"`
	Commit    string `json:"commit,omitempty"`
	StartTime string `json:"start_time"`
	Users     struct {
		Total     int `json:"total"`
		Invisible int `json:"invisible"`
		Operators int `json:"operators"`
		Unknown   int `json:"unknown"`
		Max       int `json:"max"`
	} `json:"users"`
	Channels int `json:"channels"`
	Servers  int `json:"servers"`
}
type apiChannel struct {
	Name         string `json:"name"`
	HasKey       bool   `json:"hasKey"`
	InviteOnly   bool   `json:"inviteOnly"`
	Secret       bool   `json:"secret"`
	UserCount    int    `json:"userCount"`
	Topic        string `json:"topic"`
	TopicSetAt   string `json:"topicSetAt,omitempty"`
	CreatedAt    string `json:"createdAt"`
	Registered   bool   `json:"registered"`
	Owner        string `json:"owner,omitempty"`
	RegisteredAt string `json:"registeredAt,omitempty"`
}
type listResponse struct {
	apiGeneric
	Channels []apiChannel `json:"channels"`
}

// API requests use only the exact verified private server's CA and token. Raw
// transport bodies and errors never escape the module (they may contain secrets).
func (m *Manager) api(ctx context.Context, c Config, route string, input, out any) error {
	token, e := readFile(filepath.Join(m.dir, "admin.token"), 8192, 0600)
	if e != nil || len(token) != 64 {
		return ErrRepair
	}
	for _, b := range token {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return ErrRepair
		}
	}
	conf, e := m.ScopedTLS()
	if e != nil {
		return e
	}
	data, e := json.Marshal(input)
	if e != nil || len(data) > 8192 {
		return ErrRepair
	}
	transport := &http.Transport{TLSClientConfig: conf, Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 8192}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.APIEndpoint+route, bytes.NewReader(data))
	if e != nil {
		return ErrRepair
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("Content-Type", "application/json")
	response, e := client.Do(req)
	if e != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || strings.Split(response.Header.Get("Content-Type"), ";")[0] != "application/json" {
		return ErrUnavailable
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 || strictJSON(raw, out) != nil {
		return ErrUnavailable
	}
	return nil
}
func validLocalID(id domain.ID, kind string) bool {
	s := string(id)
	if len(s) != len(kind)+33 || !strings.HasPrefix(s, kind+"_") {
		return false
	}
	for _, r := range s[len(kind)+1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func validTimestamp(s string) bool {
	t, e := time.Parse(time.RFC3339Nano, s)
	return e == nil && !t.IsZero() && t.After(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) && t.Before(time.Now().Add(5*time.Minute))
}
func (m *Manager) probe(ctx context.Context, c Config) (apiStatus, error) {
	var s apiStatus
	if e := m.api(ctx, c, "/v1/status", struct{}{}, &s); e != nil {
		return s, e
	}
	if !s.Success || s.Version != Version || !validTimestamp(s.StartTime) || s.Servers != 1 || s.Channels < 0 || s.Channels > 256 || s.GoVersion == "" || (s.Commit != "" && (len(s.Commit) < 7 || !strings.HasPrefix(SourceCommit, s.Commit))) {
		return apiStatus{}, ErrConflict
	}
	return s, nil
}

// strictJSON bounds syntax, rejects duplicate/unknown/case-alias object fields,
// and commits the response only after a single complete UTF-8 document is valid.
func strictJSON(raw []byte, out any) error {
	if !utf8.Valid(raw) {
		return ErrUnavailable
	}
	t := reflect.TypeOf(out)
	if t == nil || t.Kind() != reflect.Pointer {
		return ErrUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	nodes := 0
	if e := scanJSON(d, t.Elem(), 0, &nodes); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrUnavailable
	}
	tmp := reflect.New(t.Elem())
	if json.Unmarshal(raw, tmp.Interface()) != nil {
		return ErrUnavailable
	}
	reflect.ValueOf(out).Elem().Set(tmp.Elem())
	return nil
}
func scanJSON(d *json.Decoder, t reflect.Type, depth int, nodes *int) error {
	*nodes++
	if *nodes > 20000 || depth > 16 {
		return ErrUnavailable
	}
	tok, e := d.Token()
	if e != nil {
		return ErrUnavailable
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch v := tok.(type) {
	case json.Delim:
		if v == '{' && t.Kind() == reflect.Struct {
			fields := map[string]reflect.Type{}
			schemaFields(t, fields)
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				s, ok := key.(string)
				ft, known := fields[s]
				if e != nil || !ok || !known || seen[s] {
					return ErrUnavailable
				}
				seen[s] = true
				if e = scanJSON(d, ft, depth+1, nodes); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return ErrUnavailable
			}
			return nil
		}
		if v == '[' && t.Kind() == reflect.Slice {
			count := 0
			for d.More() {
				count++
				if count > 512 {
					return ErrUnavailable
				}
				if e = scanJSON(d, t.Elem(), depth+1, nodes); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return ErrUnavailable
			}
			return nil
		}
		return ErrUnavailable
	case nil:
		if t.Kind() == reflect.Slice {
			return nil
		}
		return ErrUnavailable
	case string:
		if t.Kind() != reflect.String || len(v) > 16384 {
			return ErrUnavailable
		}
	case bool:
		if t.Kind() != reflect.Bool {
			return ErrUnavailable
		}
	case json.Number:
		if t.Kind() != reflect.Int {
			return ErrUnavailable
		}
		if _, e := v.Int64(); e != nil {
			return ErrUnavailable
		}
	default:
		return ErrUnavailable
	}
	return nil
}
func schemaFields(t reflect.Type, fields map[string]reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			schemaFields(f.Type, fields)
			continue
		}
		key := strings.Split(f.Tag.Get("json"), ",")[0]
		if key != "" && key != "-" {
			fields[key] = f.Type
		}
	}
}
