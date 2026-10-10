// Package chatadapter implements bounded authenticated channel IO. It never
// dispatches a model turn and never derives authority from message text.
package chatadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/config"
)

const maxResponseBytes = 1 << 20

var (
	errBinding    = errors.New("channel binding could not be verified")
	errCredential = errors.New("channel credential reference could not be verified")
	errTransport  = errors.New("channel transport did not provide a bounded verified response")
)

type httpWire struct {
	client   *http.Client
	mu       sync.Mutex
	cooldown map[string]time.Time
}

func newHTTP(client *http.Client) httpWire {
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// A caller may supply a shorter timeout, but cannot remove the adapter's bound.
	if copy.Timeout <= 0 || copy.Timeout > 15*time.Second {
		copy.Timeout = 15 * time.Second
	}
	return httpWire{client: &copy, cooldown: map[string]time.Time{}}
}

func readToken(ref string) (string, error) {
	b, err := config.ReadPrivateSecret(ref)
	if err != nil {
		return "", errCredential
	}
	s := strings.TrimSpace(string(b))
	if s == "" || strings.ContainsAny(s, "\x00\r\n") {
		return "", errCredential
	}
	for _, r := range s {
		if r < 33 || r > 126 {
			return "", errCredential
		}
	}
	return s, nil
}

func endpoint(raw string) (*url.URL, error) {
	canonical, err := channels.CanonicalEndpoint(channels.Mattermost, raw)
	if err != nil || canonical != raw {
		return nil, errBinding
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.Contains(u.Path, "..") || strings.HasSuffix(u.Path, "/") || u.String() != raw {
		return nil, errBinding
	}
	if u.Host != strings.ToLower(u.Host) {
		return nil, errBinding
	}
	return u, nil
}

func apiURL(base *url.URL, path string, query url.Values) string {
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = query.Encode()
	return u.String()
}

func (w *httpWire) request(ctx context.Context, token, method, address string, body any, into any) (int, error) {
	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, errBinding
		}
		input = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, address, input)
	if err != nil {
		return 0, errBinding
	}
	// Retry-After is a per-method transport pause, never an automatic resend.
	// The private token digest is only an in-memory bucket key and is not logged.
	hash := sha256.Sum256([]byte(token))
	key := r.URL.Scheme + "://" + r.URL.Host + r.URL.Path + string(hash[:])
	w.mu.Lock()
	until := w.cooldown[key]
	w.mu.Unlock()
	if time.Now().Before(until) {
		return http.StatusTooManyRequests, nil
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept", "application/json")
	if body != nil {
		r.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	response, err := w.client.Do(r)
	if err != nil {
		return 0, errTransport
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
			if seconds > 86400 {
				seconds = 86400
			}
			w.mu.Lock()
			for key, until := range w.cooldown {
				if time.Now().After(until) {
					delete(w.cooldown, key)
				}
			}
			if len(w.cooldown) < 512 {
				w.cooldown[key] = time.Now().Add(time.Duration(seconds) * time.Second)
			}
			w.mu.Unlock()
		}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return response.StatusCode, errTransport
	}
	if into != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		if !validJSON(data) || json.Unmarshal(data, into) != nil {
			return response.StatusCode, errTransport
		}
	}
	return response.StatusCode, nil
}

// Reject duplicate keys and excessive nesting before identity fields are decoded.
func validJSON(b []byte) bool {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	nodes := 0
	var value func(int) bool
	value = func(depth int) bool {
		nodes++
		if depth > 24 || nodes > 20000 {
			return false
		}
		t, err := d.Token()
		if err != nil {
			return false
		}
		mark, ok := t.(json.Delim)
		if !ok {
			return true
		}
		switch mark {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return false
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !value(depth + 1) {
					return false
				}
			}
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
		default:
			return false
		}
		end, err := d.Token()
		return err == nil && ((mark == '{' && end == json.Delim('}')) || (mark == '[' && end == json.Delim(']')))
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func evidence(b channels.Binding, realm, self, source string) channels.Evidence {
	return channels.Evidence{Provider: b.Provider, Endpoint: b.Endpoint, RealmID: realm, SelfID: self, ChannelID: b.ChannelID, RootID: b.RootID, Source: source, ObservedAt: time.Now().UTC()}
}

func matchIdentity(b channels.Binding, realm, self string) bool {
	return channels.ValidID(realm) && channels.ValidID(self) && (b.RealmID == "" || realm == b.RealmID) && (b.SelfID == "" || self == b.SelfID)
}

func boundPage(cursor string, limit int) bool {
	return channels.ValidCursor(cursor) && limit > 0 && limit <= channels.MaxPageSize
}

func sendInput(b channels.Binding, d channels.Delivery, m channels.Message) bool {
	return channels.ValidID(b.RealmID) && channels.ValidID(b.SelfID) && b.Active && d.ID != "" && d.ID == d.MessageID && d.MessageID == m.ID && d.BindingID == b.ID && m.BindingID == b.ID && m.ThreadID == b.ThreadID && channels.ValidText(m.Text)
}

func failed() channels.SendResult {
	return channels.SendResult{State: channels.Failed, Code: "remote_rejected"}
}
func uncertain(e channels.Evidence) channels.SendResult {
	return channels.SendResult{State: channels.Uncertain, Code: "transport_uncertain", Evidence: e}
}
func unconfirmed(e channels.Evidence) channels.SendResult {
	return channels.SendResult{State: channels.Uncertain, Code: "unconfirmed", Evidence: e}
}

func httpSendFailure(status int, err error, e channels.Evidence) channels.SendResult {
	if err == nil && (status == 400 || status == 401 || status == 403 || status == 404 || status == 429) {
		r := failed()
		r.Evidence = e
		return r
	}
	return uncertain(e)
}

func New(provider channels.Provider) (channels.Adapter, error) {
	switch provider {
	case channels.Mattermost:
		return NewMattermost(nil), nil
	case channels.Slack:
		return NewSlack(nil), nil
	case channels.IRC:
		return NewIRC(), nil
	default:
		return nil, errBinding
	}
}
