package chatadapter

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
)

type Slack struct{ wire httpWire }

// NewSlack accepts an injected client, including a test RoundTripper routing
// the pinned Slack endpoint to an isolated TLS fixture. The binding endpoint
// is always exactly https://slack.com/api, including during tests.
func NewSlack(client *http.Client) *Slack { return &Slack{wire: newHTTP(client)} }
func (a *Slack) Close() error             { return a.wire.close() }

type slackMeta struct {
	EventType string            `json:"event_type"`
	Payload   map[string]string `json:"event_payload"`
}
type slackMessage struct {
	Type     string     `json:"type"`
	Subtype  string     `json:"subtype"`
	User     string     `json:"user"`
	BotID    string     `json:"bot_id"`
	TS       string     `json:"ts"`
	ThreadTS string     `json:"thread_ts"`
	Text     string     `json:"text"`
	Metadata *slackMeta `json:"metadata"`
}
type slackPage struct {
	OK       bool           `json:"ok"`
	Error    string         `json:"error"`
	Messages []slackMessage `json:"messages"`
	HasMore  bool           `json:"has_more"`
	Metadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}
type slackAuth struct {
	OK   bool   `json:"ok"`
	Team string `json:"team_id"`
	User string `json:"user_id"`
	Bot  string `json:"bot_id"`
}
type slackCursor struct{ Binding, Cursor, Oldest, Newest string }

var slackTimestamp = regexp.MustCompile(`^[0-9]{1,20}\.[0-9]{1,10}$`)
var errSlackCursor = errors.New("Slack reply cursor expired")

func (a *Slack) page(ctx context.Context, b channels.Binding, base *url.URL, token string, query url.Values) (slackPage, error) {
	var page slackPage
	status, err := a.wire.request(ctx, token, http.MethodGet, apiURL(base, "/conversations.replies", query), nil, &page)
	if err == nil && status == http.StatusOK && !page.OK && page.Error == "invalid_cursor" {
		return slackPage{}, errSlackCursor
	}
	if err != nil || status != http.StatusOK || !page.OK || len(page.Messages) > channels.MaxPageSize || !channels.ValidCursor(page.Metadata.NextCursor) {
		return slackPage{}, errTransport
	}
	for _, m := range page.Messages {
		if !slackTimestamp.MatchString(m.TS) || (m.TS == b.RootID && m.ThreadTS != "" && m.ThreadTS != b.RootID) || (m.TS != b.RootID && m.ThreadTS != b.RootID) {
			return slackPage{}, errBinding
		}
	}
	return page, nil
}

func (a *Slack) verify(ctx context.Context, b channels.Binding, token string, checkRoot bool) (*url.URL, channels.Evidence, slackAuth, error) {
	base, err := endpoint(b.Endpoint)
	if err != nil || b.Endpoint != "https://slack.com/api" || b.Provider != channels.Slack || !segment(b.ChannelID) || !slackTimestamp.MatchString(b.RootID) {
		return nil, channels.Evidence{}, slackAuth{}, errBinding
	}
	var auth slackAuth
	status, err := a.wire.request(ctx, token, http.MethodPost, apiURL(base, "/auth.test", nil), map[string]any{}, &auth)
	if err != nil || status != http.StatusOK || !auth.OK || !matchIdentity(b, auth.Team, auth.User) {
		return nil, channels.Evidence{}, slackAuth{}, errBinding
	}
	if checkRoot {
		// Initial binding proof only: repeating this before Fetch would consume
		// the entire one-request-per-minute replies allowance on affected apps.
		page, err := a.page(ctx, b, base, token, url.Values{"channel": {b.ChannelID}, "ts": {b.RootID}, "limit": {"1"}})
		if err != nil || len(page.Messages) != 1 || page.Messages[0].TS != b.RootID || page.Messages[0].Type != "message" || (page.Messages[0].Subtype != "" && page.Messages[0].Subtype != "bot_message") {
			return nil, channels.Evidence{}, slackAuth{}, errBinding
		}
	}
	return base, evidence(b, auth.Team, auth.User, "slack_web_api_authenticated_root"), auth, nil
}

func (a *Slack) Probe(ctx context.Context, b channels.Binding) (channels.Evidence, error) {
	token, err := readToken(b.CredentialRef)
	if err != nil {
		return channels.Evidence{}, err
	}
	_, e, _, err := a.verify(ctx, b, token, true)
	return e, err
}

func slackActor(m slackMessage, auth slackAuth) string {
	if channels.ValidID(m.User) {
		return m.User
	}
	if channels.ValidID(m.BotID) {
		if m.BotID == auth.Bot && auth.Bot != "" {
			return auth.User
		}
		return "bot:" + m.BotID
	}
	return ""
}

func slackTime(ts string) (time.Time, bool) {
	parts := strings.Split(ts, ".")
	if len(parts) != 2 {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	fraction := parts[1]
	if len(fraction) > 9 {
		fraction = fraction[:9]
	}
	fraction += strings.Repeat("0", 9-len(fraction))
	nanos, err := strconv.ParseInt(fraction, 10, 64)
	return time.Unix(seconds, nanos).UTC(), err == nil
}

func (a *Slack) Fetch(ctx context.Context, b channels.Binding, cursor string, limit int) (channels.FetchResult, error) {
	if !b.Active || b.SelfID == "" || b.RealmID == "" || b.ActivationCutoff.IsZero() || !boundPage(cursor, limit) {
		return channels.FetchResult{}, errBinding
	}
	cutoff := fmt.Sprintf("%d.%06d", b.ActivationCutoff.Unix(), b.ActivationCutoff.Nanosecond()/1000)
	position := slackCursor{Binding: string(b.ID), Oldest: cutoff, Newest: cutoff}
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || !validJSON(decoded) || json.Unmarshal(decoded, &position) != nil || position.Binding != string(b.ID) || !channels.ValidCursor(position.Cursor) || !slackTimestamp.MatchString(position.Oldest) || !slackTimestamp.MatchString(position.Newest) {
			return channels.FetchResult{}, errBinding
		}
	}
	// 15 also works for installations subject to the commercial distribution cap.
	if limit > 15 {
		limit = 15
	}
	token, err := readToken(b.CredentialRef)
	if err != nil {
		return channels.FetchResult{}, err
	}
	base, e, auth, err := a.verify(ctx, b, token, false)
	if err != nil {
		return channels.FetchResult{}, err
	}
	query := url.Values{"channel": {b.ChannelID}, "ts": {b.RootID}, "limit": {strconv.Itoa(limit)}, "oldest": {position.Oldest}, "inclusive": {"true"}, "include_all_metadata": {"true"}}
	if position.Cursor != "" {
		query.Set("cursor", position.Cursor)
	}
	page, err := a.page(ctx, b, base, token, query)
	if err == errSlackCursor && position.Cursor != "" {
		// One bounded read can restart an expired cursor from its retained
		// watermark. Exact source-ID deduplication absorbs inclusive overlap.
		query.Del("cursor")
		position.Cursor = ""
		page, err = a.page(ctx, b, base, token, query)
	}
	if err != nil || len(page.Messages) > limit || (position.Cursor != "" && page.Metadata.NextCursor == position.Cursor) {
		return channels.FetchResult{}, errTransport
	}
	result := channels.FetchResult{Evidence: e, Messages: []channels.RemoteMessage{}, HasMore: page.HasMore || page.Metadata.NextCursor != ""}
	seen := map[string]bool{}
	for _, remote := range page.Messages {
		if seen[remote.TS] {
			return channels.FetchResult{}, errBinding
		}
		seen[remote.TS] = true
		if remote.TS == b.RootID || remote.Type != "message" || (remote.Subtype != "" && remote.Subtype != "bot_message") {
			continue
		}
		at, ok := slackTime(remote.TS)
		actor := slackActor(remote, auth)
		if ok {
			current, _ := slackTime(position.Newest)
			if at.After(current) {
				position.Newest = remote.TS
			}
		}
		if !ok || !channels.ValidID(actor) || !channels.ValidText(remote.Text) {
			continue
		}
		result.Messages = append(result.Messages, channels.RemoteMessage{ID: remote.TS, ActorID: actor, RealmID: e.RealmID, ChannelID: b.ChannelID, RootID: b.RootID, Text: remote.Text, CreatedAt: at, Historical: !at.After(b.ActivationCutoff)})
	}
	// Retain a timestamp watermark after exhausting short-lived API cursors.
	// Inclusive overlap is intentional; the store deduplicates exact source IDs.
	position.Cursor = page.Metadata.NextCursor
	if position.Cursor == "" && !result.HasMore {
		position.Oldest = position.Newest
	}
	encoded, _ := json.Marshal(position)
	result.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	if !channels.ValidCursor(result.NextCursor) || result.HasMore && position.Cursor == "" {
		return channels.FetchResult{}, errTransport
	}
	return result, nil
}

func slackRejected(code string) bool {
	switch code {
	case "invalid_auth", "not_authed", "token_revoked", "token_expired", "missing_scope", "channel_not_found", "not_in_channel", "is_archived", "no_permission", "thread_not_found", "cannot_reply_to_message", "restricted_action", "rate_limited", "ratelimited", "no_text", "invalid_arguments", "invalid_metadata_format", "invalid_metadata_schema", "metadata_must_be_sent_from_app", "metadata_too_large":
		return true
	}
	return false
}

func slackReceipt(p slackMessage, b channels.Binding, d channels.Delivery, m channels.Message, auth slackAuth) bool {
	_, validTime := slackTime(p.TS)
	return validTime && p.TS != b.RootID && p.Text == m.Text && p.Type == "message" && (p.Subtype == "" || p.Subtype == "bot_message") && slackTimestamp.MatchString(p.TS) && p.ThreadTS == b.RootID && slackActor(p, auth) == auth.User && p.Metadata != nil && p.Metadata.EventType == "keelmux_reply" && p.Metadata.Payload["delivery_id"] == string(d.ID) && p.Metadata.Payload["payload_hash"] == hex.EncodeToString(d.PayloadHash[:])
}

func (a *Slack) Send(ctx context.Context, b channels.Binding, d channels.Delivery, m channels.Message) (channels.SendResult, error) {
	if !sendInput(b, d, m) {
		return failed(), nil
	}
	token, err := readToken(b.CredentialRef)
	if err != nil {
		return failed(), nil
	}
	base, e, auth, err := a.verify(ctx, b, token, false)
	if err != nil {
		return failed(), nil
	}
	body := map[string]any{"channel": b.ChannelID, "thread_ts": b.RootID, "text": m.Text, "reply_broadcast": false, "unfurl_links": false, "unfurl_media": false, "metadata": slackMeta{EventType: "keelmux_reply", Payload: map[string]string{"delivery_id": string(d.ID), "payload_hash": hex.EncodeToString(d.PayloadHash[:])}}}
	var posted struct {
		OK      bool         `json:"ok"`
		Error   string       `json:"error"`
		Channel string       `json:"channel"`
		TS      string       `json:"ts"`
		Message slackMessage `json:"message"`
	}
	status, err := a.wire.request(ctx, token, http.MethodPost, apiURL(base, "/chat.postMessage", nil), body, &posted)
	if err != nil || status != http.StatusOK {
		return httpSendFailure(status, err, e), nil
	}
	if !posted.OK {
		if slackRejected(posted.Error) {
			r := failed()
			r.Evidence = e
			return r, nil
		}
		return uncertain(e), nil
	}
	if posted.Channel != b.ChannelID || posted.TS != posted.Message.TS || !slackReceipt(posted.Message, b, d, m, auth) {
		return uncertain(e), nil
	}
	return channels.SendResult{State: channels.Delivered, RemoteID: posted.TS, Evidence: e}, nil
}

func (a *Slack) Reconcile(ctx context.Context, b channels.Binding, d channels.Delivery, m channels.Message) (channels.SendResult, error) {
	if !receiptInput(b, d, m) {
		return unconfirmed(channels.Evidence{}), nil
	}
	token, err := readToken(b.CredentialRef)
	if err != nil {
		return unconfirmed(channels.Evidence{}), nil
	}
	base, e, auth, err := a.verify(ctx, b, token, false)
	if err != nil {
		return unconfirmed(channels.Evidence{}), nil
	}
	query := url.Values{"channel": {b.ChannelID}, "ts": {b.RootID}, "limit": {"15"}, "include_all_metadata": {"true"}}
	if d.RemoteID != "" {
		if !slackTimestamp.MatchString(d.RemoteID) {
			return unconfirmed(e), nil
		}
		query.Set("oldest", d.RemoteID)
		query.Set("latest", d.RemoteID)
		query.Set("inclusive", "true")
	}
	page, err := a.page(ctx, b, base, token, query)
	if err != nil {
		return unconfirmed(e), nil
	}
	found := ""
	for _, p := range page.Messages {
		if slackReceipt(p, b, d, m, auth) {
			if found != "" {
				return unconfirmed(e), nil
			}
			found = p.TS
		}
	}
	if found != "" {
		return channels.SendResult{State: channels.Delivered, RemoteID: found, Evidence: e}, nil
	}
	return unconfirmed(e), nil
}
