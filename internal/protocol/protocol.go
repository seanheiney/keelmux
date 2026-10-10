// Package protocol defines the private versioned local control API shared by clients.
package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/seanheiney/agent-backplane/internal/domain"
	"net"
	"net/http"
	"time"
)

const Version = "bp/1"

type LaunchRequest struct {
	OperationID     domain.ID `json:"operation_id"`
	Name            string    `json:"name"`
	Workspace       string    `json:"workspace"`
	Provider        string    `json:"provider"`
	ProviderVersion string    `json:"provider_version"`
	Argv            []string  `json:"argv"`
}
type SessionRequest struct {
	SessionID   domain.ID `json:"session_id"`
	ExecutionID domain.ID `json:"execution_id"`
	Generation  uint64    `json:"generation"`
	OperationID domain.ID `json:"operation_id"`
}
type LeaseRequest struct {
	SessionID   domain.ID `json:"session_id"`
	ExecutionID domain.ID `json:"execution_id"`
	Generation  uint64    `json:"generation"`
	OperationID domain.ID `json:"operation_id"`
	ClientID    string    `json:"client_id"`
	ReadOnly    bool      `json:"read_only"`
	Token       string    `json:"token,omitempty"`
}

// Link requires a runtime-correlated context, not a model-supplied native thread ID.
type LinkRequest struct {
	SessionID   domain.ID `json:"session_id"`
	ExecutionID domain.ID `json:"execution_id"`
	OperationID domain.ID `json:"operation_id"`
	Off         bool      `json:"off"`
}
type Inventory struct {
	Version  string           `json:"version"`
	Sessions []domain.Session `json:"sessions"`
}
type LaunchResponse struct {
	Session   domain.Session   `json:"session"`
	Operation domain.Operation `json:"operation"`
}
type Error struct {
	Message string `json:"error"`
}
type Client struct {
	HTTP    *http.Client
	BaseURL string
}

func NewClient(socket string) *Client {
	t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &Client{HTTP: &http.Client{Transport: t, Timeout: 20 * time.Second}, BaseURL: "http://backplane"}
}
func (c *Client) Call(ctx context.Context, method, path string, in, out any) error {
	var b bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&b).Encode(in); err != nil {
			return err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, &b)
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Backplane-Protocol", Version)
	res, err := c.HTTP.Do(r)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		var e Error
		_ = json.NewDecoder(res.Body).Decode(&e)
		return fmt.Errorf("%s: %s", res.Status, e.Message)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}
func (c *Client) List(ctx context.Context) ([]domain.Session, error) {
	var v Inventory
	err := c.Call(ctx, "GET", "/v1/sessions", nil, &v)
	return v.Sessions, err
}
func (c *Client) Launch(ctx context.Context, r LaunchRequest) (LaunchResponse, error) {
	var v LaunchResponse
	err := c.Call(ctx, "POST", "/v1/launch", r, &v)
	return v, err
}
func (c *Client) Link(ctx context.Context, r LinkRequest) (domain.Session, error) {
	var v domain.Session
	err := c.Call(ctx, "POST", "/v1/link", r, &v)
	return v, err
}
func (c *Client) Attach(ctx context.Context, r LeaseRequest) (domain.AttachSpec, error) {
	var v domain.AttachSpec
	err := c.Call(ctx, "POST", "/v1/attach", r, &v)
	return v, err
}
func (c *Client) Release(ctx context.Context, r LeaseRequest) error {
	return c.Call(ctx, "POST", "/v1/release", r, nil)
}
func (c *Client) Reconcile(ctx context.Context) error {
	return c.Call(ctx, "POST", "/v1/reconcile", struct{}{}, nil)
}
func (c *Client) Stop(ctx context.Context, r SessionRequest) error {
	return c.Call(ctx, "POST", "/v1/stop", r, nil)
}
