package protocol

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
)

type ChannelBindRequest struct {
	ID            domain.ID         `json:"binding_id"`
	SessionID     domain.ID         `json:"session_id"`
	Provider      channels.Provider `json:"provider"`
	Endpoint      string            `json:"endpoint"`
	ChannelID     string            `json:"channel_id"`
	RootID        string            `json:"root_id"`
	CredentialRef string            `json:"credential_file"`
	Allowlist     []string          `json:"allow_users"`
}

// ChannelStatus projects routing and receipt evidence without credential bytes
// or references. These endpoints are local-home only, outside entry inventory.
type ChannelStatus struct {
	ID               domain.ID         `json:"binding_id"`
	SessionID        domain.ID         `json:"session_id"`
	ThreadID         domain.ID         `json:"thread_id"`
	Provider         channels.Provider `json:"provider"`
	Endpoint         string            `json:"endpoint"`
	RealmID          string            `json:"realm_id"`
	SelfID           string            `json:"bot_identity"`
	ChannelID        string            `json:"channel_id"`
	RootID           string            `json:"root_id,omitempty"`
	Allowlist        []string          `json:"allow_users"`
	Active           bool              `json:"active"`
	ActivationCutoff time.Time         `json:"active_since"`
	SemanticDispatch bool              `json:"semantic_dispatch"`
}

func ProjectChannel(b channels.Binding) ChannelStatus {
	return ChannelStatus{ID: b.ID, SessionID: b.SessionID, ThreadID: b.ThreadID, Provider: b.Provider, Endpoint: b.Endpoint, RealmID: b.RealmID, SelfID: b.SelfID, ChannelID: b.ChannelID, RootID: b.RootID, Allowlist: append([]string{}, b.Allowlist...), Active: b.Active, ActivationCutoff: b.ActivationCutoff}
}

type ChannelIDRequest struct {
	ID domain.ID `json:"binding_id"`
}
type ChannelSendRequest struct {
	ID        domain.ID `json:"message_id"`
	BindingID domain.ID `json:"binding_id"`
	Text      string    `json:"text"`
}
type ChannelSendResponse struct {
	Message  channels.Message  `json:"message"`
	Delivery channels.Delivery `json:"delivery"`
}
type ChannelSyncResponse struct {
	BindingID domain.ID          `json:"binding_id"`
	Imported  int                `json:"imported"`
	HasMore   bool               `json:"more_history"`
	Delivery  *channels.Delivery `json:"delivery,omitempty"`
}
type ChannelHistory struct {
	ThreadID         domain.ID          `json:"thread_id"`
	Messages         []channels.Message `json:"messages"`
	SemanticDispatch bool               `json:"semantic_dispatch"`
}

func (c *Client) Channels(ctx context.Context) ([]ChannelStatus, error) {
	var out []ChannelStatus
	err := c.Call(ctx, "GET", "/v1/channels", nil, &out)
	return out, err
}
func (c *Client) BindChannel(ctx context.Context, r ChannelBindRequest) (ChannelStatus, error) {
	var out ChannelStatus
	err := c.Call(ctx, "POST", "/v1/channels/bind", r, &out)
	return out, err
}
func (c *Client) DisableChannel(ctx context.Context, id domain.ID) error {
	return c.Call(ctx, "POST", "/v1/channels/disable", ChannelIDRequest{ID: id}, nil)
}
func (c *Client) SyncChannel(ctx context.Context, id domain.ID) (ChannelSyncResponse, error) {
	var out ChannelSyncResponse
	err := c.Call(ctx, "POST", "/v1/channels/sync", ChannelIDRequest{ID: id}, &out)
	return out, err
}
func (c *Client) SendChannelMessage(ctx context.Context, r ChannelSendRequest) (ChannelSendResponse, error) {
	var out ChannelSendResponse
	err := c.Call(ctx, "POST", "/v1/channels/send", r, &out)
	return out, err
}
func (c *Client) ChannelHistory(ctx context.Context, thread domain.ID, after int64, limit int) (ChannelHistory, error) {
	var out ChannelHistory
	q := url.Values{"thread": {string(thread)}, "after": {strconv.FormatInt(after, 10)}, "limit": {strconv.Itoa(limit)}}
	err := c.Call(ctx, "GET", "/v1/channels/history?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) ChannelDelivery(ctx context.Context, id domain.ID) (channels.Delivery, error) {
	var out channels.Delivery
	err := c.Call(ctx, "GET", "/v1/channels/delivery?id="+url.QueryEscape(string(id)), nil, &out)
	return out, err
}
func (c *Client) ReconcileChannelDelivery(ctx context.Context, id domain.ID) (channels.Delivery, error) {
	var out channels.Delivery
	err := c.Call(ctx, "POST", "/v1/channels/reconcile", struct {
		ID domain.ID `json:"message_id"`
	}{id}, &out)
	return out, err
}
