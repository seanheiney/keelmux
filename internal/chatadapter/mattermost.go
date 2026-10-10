package chatadapter

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
)

type Mattermost struct{ wire httpWire }

// NewMattermost injects a client for isolated TLS fixtures; endpoint validation
// and redirect refusal are identical for injected and production clients.
func NewMattermost(client *http.Client) *Mattermost { return &Mattermost{wire: newHTTP(client)} }
func (*Mattermost) Close() error                    { return nil }

type mmPost struct {
	ID        string                     `json:"id"`
	UserID    string                     `json:"user_id"`
	ChannelID string                     `json:"channel_id"`
	RootID    string                     `json:"root_id"`
	Text      string                     `json:"message"`
	CreatedAt int64                      `json:"create_at"`
	DeletedAt int64                      `json:"delete_at"`
	Type      string                     `json:"type"`
	Props     map[string]json.RawMessage `json:"props"`
}
type mmList struct {
	Order          []string          `json:"order"`
	Posts          map[string]mmPost `json:"posts"`
	InaccessibleAt int64             `json:"first_inaccessible_post_time"`
	HasNext        *bool             `json:"has_next"`
}
type mmCursor struct {
	Binding, Post string
	At            int64
}

func segment(s string) bool {
	if !channels.ValidID(s) {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (a *Mattermost) verify(ctx context.Context, b channels.Binding, token string) (*url.URL, channels.Evidence, error) {
	base, err := endpoint(b.Endpoint)
	if err != nil || b.Provider != channels.Mattermost || !segment(b.ChannelID) || !segment(b.RootID) {
		return nil, channels.Evidence{}, errBinding
	}
	var me struct {
		ID string `json:"id"`
	}
	status, err := a.wire.request(ctx, token, http.MethodGet, apiURL(base, "/api/v4/users/me", nil), nil, &me)
	if err != nil || status != http.StatusOK || !segment(me.ID) || b.SelfID != "" && me.ID != b.SelfID {
		return nil, channels.Evidence{}, errBinding
	}
	var channel struct {
		ID       string `json:"id"`
		TeamID   string `json:"team_id"`
		Type     string `json:"type"`
		DeleteAt int64  `json:"delete_at"`
	}
	status, err = a.wire.request(ctx, token, http.MethodGet, apiURL(base, "/api/v4/channels/"+b.ChannelID, nil), nil, &channel)
	if err != nil || status != http.StatusOK || channel.ID != b.ChannelID || channel.DeleteAt != 0 || (channel.Type != "O" && channel.Type != "P") || !matchIdentity(b, channel.TeamID, me.ID) {
		return nil, channels.Evidence{}, errBinding
	}
	var root mmPost
	status, err = a.wire.request(ctx, token, http.MethodGet, apiURL(base, "/api/v4/posts/"+b.RootID, nil), nil, &root)
	if err != nil || status != http.StatusOK || root.ID != b.RootID || root.ChannelID != b.ChannelID || root.RootID != "" || root.DeletedAt != 0 || root.CreatedAt <= 0 {
		return nil, channels.Evidence{}, errBinding
	}
	return base, evidence(b, channel.TeamID, me.ID, "mattermost_rest_v4_authenticated_root"), nil
}

func (a *Mattermost) Probe(ctx context.Context, b channels.Binding) (channels.Evidence, error) {
	token, err := readToken(b.CredentialRef)
	if err != nil {
		return channels.Evidence{}, err
	}
	_, e, err := a.verify(ctx, b, token)
	return e, err
}

func (a *Mattermost) thread(ctx context.Context, b channels.Binding, base *url.URL, token string, query url.Values) ([]mmPost, bool, error) {
	var list mmList
	status, err := a.wire.request(ctx, token, http.MethodGet, apiURL(base, "/api/v4/posts/"+b.RootID+"/thread", query), nil, &list)
	if err != nil || status != http.StatusOK || list.InaccessibleAt != 0 || len(list.Order) > channels.MaxPageSize+1 || len(list.Posts) > channels.MaxPageSize+1 {
		return nil, false, errTransport
	}
	root, ok := list.Posts[b.RootID]
	if !ok || root.ID != b.RootID || root.ChannelID != b.ChannelID || root.RootID != "" || root.DeletedAt != 0 {
		return nil, false, errBinding
	}
	posts := make([]mmPost, 0, len(list.Order))
	seen := map[string]bool{}
	for _, id := range list.Order {
		p, ok := list.Posts[id]
		if !ok || seen[id] || p.ID != id || !segment(id) || p.ChannelID != b.ChannelID || (id != b.RootID && p.RootID != b.RootID) || (id == b.RootID && p.RootID != "") {
			return nil, false, errBinding
		}
		seen[id] = true
		if id != b.RootID {
			posts = append(posts, p)
		}
	}
	if len(seen) != len(list.Posts) {
		return nil, false, errBinding
	}
	sort.SliceStable(posts, func(i, j int) bool {
		if posts[i].CreatedAt == posts[j].CreatedAt {
			return posts[i].ID < posts[j].ID
		}
		return posts[i].CreatedAt < posts[j].CreatedAt
	})
	limit, _ := strconv.Atoi(query.Get("perPage"))
	if limit < 1 || limit > channels.MaxPageSize || len(posts) > limit {
		return nil, false, errTransport
	}
	hasMore := len(posts) == limit
	if list.HasNext != nil {
		hasMore = *list.HasNext
	}
	if hasMore && len(posts) == 0 {
		return nil, false, errTransport
	}
	return posts, hasMore, nil
}

func (a *Mattermost) Fetch(ctx context.Context, b channels.Binding, cursor string, limit int) (channels.FetchResult, error) {
	if !b.Active || b.SelfID == "" || b.RealmID == "" || b.ActivationCutoff.IsZero() || !boundPage(cursor, limit) {
		return channels.FetchResult{}, errBinding
	}
	position := mmCursor{Binding: string(b.ID), At: b.ActivationCutoff.UnixMilli()}
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || !validJSON(decoded) || json.Unmarshal(decoded, &position) != nil || position.Binding != string(b.ID) || position.At <= 0 || (position.Post != "" && !segment(position.Post)) {
			return channels.FetchResult{}, errBinding
		}
	}
	token, err := readToken(b.CredentialRef)
	if err != nil {
		return channels.FetchResult{}, err
	}
	base, e, err := a.verify(ctx, b, token)
	if err != nil {
		return channels.FetchResult{}, err
	}
	query := url.Values{"perPage": {strconv.Itoa(limit)}, "direction": {"down"}, "fromCreateAt": {strconv.FormatInt(position.At, 10)}}
	if position.Post != "" {
		query.Set("fromPost", position.Post)
	}
	posts, hasMore, err := a.thread(ctx, b, base, token, query)
	if err != nil {
		return channels.FetchResult{}, err
	}
	result := channels.FetchResult{Evidence: e, Messages: []channels.RemoteMessage{}, HasMore: hasMore}
	for _, p := range posts {
		if p.CreatedAt <= 0 || p.CreatedAt < position.At || p.CreatedAt == position.At && p.ID <= position.Post {
			return channels.FetchResult{}, errBinding
		}
		position.At = p.CreatedAt
		position.Post = p.ID
		if p.DeletedAt != 0 || p.Type != "" || !channels.ValidID(p.UserID) || !channels.ValidText(p.Text) {
			continue
		}
		result.Messages = append(result.Messages, channels.RemoteMessage{ID: p.ID, ActorID: p.UserID, RealmID: e.RealmID, ChannelID: b.ChannelID, RootID: b.RootID, Text: p.Text, CreatedAt: time.UnixMilli(p.CreatedAt).UTC(), Historical: !time.UnixMilli(p.CreatedAt).After(b.ActivationCutoff)})
	}
	encoded, _ := json.Marshal(position)
	result.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	if !channels.ValidCursor(result.NextCursor) {
		return channels.FetchResult{}, errBinding
	}
	return result, nil
}

func (a *Mattermost) Send(ctx context.Context, b channels.Binding, d channels.Delivery, m channels.Message) (channels.SendResult, error) {
	if !sendInput(b, d, m) {
		return failed(), nil
	}
	token, err := readToken(b.CredentialRef)
	if err != nil {
		return failed(), nil
	}
	base, e, err := a.verify(ctx, b, token)
	if err != nil {
		return failed(), nil
	}
	body := map[string]any{"channel_id": b.ChannelID, "root_id": b.RootID, "message": m.Text, "pending_post_id": string(d.ID), "props": map[string]string{"keelmux_delivery_id": string(d.ID), "keelmux_payload_hash": hex.EncodeToString(d.PayloadHash[:])}}
	var posted mmPost
	status, err := a.wire.request(ctx, token, http.MethodPost, apiURL(base, "/api/v4/posts", nil), body, &posted)
	if err != nil || status != http.StatusCreated {
		return httpSendFailure(status, err, e), nil
	}
	if !mmReceipt(posted, b, d, e) {
		return uncertain(e), nil
	}
	return channels.SendResult{State: channels.Delivered, RemoteID: posted.ID, Evidence: e}, nil
}

func mmReceipt(p mmPost, b channels.Binding, d channels.Delivery, e channels.Evidence) bool {
	return segment(p.ID) && p.UserID == e.SelfID && p.ChannelID == b.ChannelID && p.RootID == b.RootID && p.DeletedAt == 0 && rawString(p.Props["keelmux_delivery_id"]) == string(d.ID) && rawString(p.Props["keelmux_payload_hash"]) == hex.EncodeToString(d.PayloadHash[:])
}
func rawString(b json.RawMessage) string {
	var s string
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return s
}

func (a *Mattermost) Reconcile(ctx context.Context, b channels.Binding, d channels.Delivery, m channels.Message) (channels.SendResult, error) {
	if !sendInput(b, d, m) {
		return unconfirmed(channels.Evidence{}), nil
	}
	token, err := readToken(b.CredentialRef)
	if err != nil {
		return unconfirmed(channels.Evidence{}), nil
	}
	base, e, err := a.verify(ctx, b, token)
	if err != nil {
		return unconfirmed(channels.Evidence{}), nil
	}
	if d.RemoteID != "" && segment(d.RemoteID) {
		var p mmPost
		status, err := a.wire.request(ctx, token, http.MethodGet, apiURL(base, "/api/v4/posts/"+d.RemoteID, nil), nil, &p)
		if err == nil && status == http.StatusOK && p.ID == d.RemoteID && mmReceipt(p, b, d, e) {
			return channels.SendResult{State: channels.Delivered, RemoteID: p.ID, Evidence: e}, nil
		}
		return unconfirmed(e), nil
	}
	posts, _, err := a.thread(ctx, b, base, token, url.Values{"perPage": {strconv.Itoa(channels.MaxPageSize)}, "direction": {"up"}})
	if err != nil {
		return unconfirmed(e), nil
	}
	found := ""
	for _, p := range posts {
		if mmReceipt(p, b, d, e) {
			if found != "" {
				return unconfirmed(e), nil
			}
			found = p.ID
		}
	}
	if found != "" {
		return channels.SendResult{State: channels.Delivered, RemoteID: found, Evidence: e}, nil
	}
	return unconfirmed(e), nil
}
