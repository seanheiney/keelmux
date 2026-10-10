package protocol

import (
	"context"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
)

type IRCDInitRequest struct {
	IRCPort int `json:"irc_port"`
	APIPort int `json:"api_port"`
}
type IRCDStartRequest struct {
	OperationID domain.ID `json:"operation_id"`
}
type IRCDStopRequest struct {
	Instance domain.ID `json:"expected_instance"`
}
type IRCDRestartRequest struct {
	Instance    domain.ID `json:"expected_instance"`
	OperationID domain.ID `json:"operation_id"`
}
type IRCDAccountRequest struct {
	Account string `json:"account"`
}
type IRCDBindRequest struct {
	ID          domain.ID `json:"binding_id"`
	SessionID   domain.ID `json:"session_id"`
	OperationID domain.ID `json:"operation_id"`
	Allowlist   []string  `json:"allow_users"`
}

// IRCDConnection deliberately exposes only local connection configuration.
// The credential reference is an owned private file, never credential bytes.
// Generic status and channel status projections exclude this reference.
type IRCDConnection struct {
	ServerID      domain.ID `json:"server_id"`
	Endpoint      string    `json:"endpoint"`
	RealmID       string    `json:"realm_id"`
	Account       string    `json:"account"`
	CAFile        string    `json:"ca_file"`
	CredentialRef string    `json:"credential_file"`
	Version       string    `json:"version"`
	Source        string    `json:"source"`
	Target        string    `json:"target"`
}
type IRCDStatus struct {
	ServerID      domain.ID `json:"server_id"`
	Endpoint      string    `json:"endpoint"`
	RealmID       string    `json:"realm_id"`
	Account       string    `json:"account"`
	Instance      domain.ID `json:"instance"`
	OperationID   domain.ID `json:"operation_id"`
	State         string    `json:"state"`
	Alive         bool      `json:"alive"`
	Version       string    `json:"version"`
	Source        string    `json:"source"`
	Target        string    `json:"target"`
	StartTime     string    `json:"start_time"`
	GoVersion     string    `json:"go_version"`
	RuntimeCommit string    `json:"runtime_commit"`
	ObservedAt    time.Time `json:"observed_at"`
}

func ProjectIRCDConnection(c ircd.Config) IRCDConnection {
	return IRCDConnection{ServerID: c.ID, Endpoint: c.Endpoint, RealmID: c.RealmID, Account: c.Account, CAFile: c.CAFile, CredentialRef: c.CredentialRef, Version: c.Runtime.Version, Source: c.Runtime.Source, Target: c.Runtime.Target}
}
func ProjectIRCDStatus(s ircd.Status) IRCDStatus {
	return IRCDStatus{ServerID: s.Config.ID, Endpoint: s.Config.Endpoint, RealmID: s.Config.RealmID, Account: s.Config.Account, Instance: s.Instance, OperationID: s.OperationID, State: s.State, Alive: s.Alive, Version: s.Version, Source: s.Config.Runtime.Source, Target: s.Config.Runtime.Target, StartTime: s.StartTime, GoVersion: s.GoVersion, RuntimeCommit: s.RuntimeCommit, ObservedAt: s.ObservedAt}
}
func (c *Client) InitIRCD(ctx context.Context, in IRCDInitRequest) (IRCDConnection, error) {
	var out IRCDConnection
	err := c.callChannel(ctx, "POST", "/v1/ircd/init", in, &out)
	return out, err
}
func (c *Client) IRCD(ctx context.Context) (IRCDStatus, error) {
	var out IRCDStatus
	err := c.callChannel(ctx, "GET", "/v1/ircd/status", nil, &out)
	return out, err
}
func (c *Client) StartIRCD(ctx context.Context, id domain.ID) (IRCDStatus, error) {
	var out IRCDStatus
	err := c.callChannel(ctx, "POST", "/v1/ircd/start", IRCDStartRequest{OperationID: id}, &out)
	return out, err
}
func (c *Client) StopIRCD(ctx context.Context, instance domain.ID) error {
	return c.callChannel(ctx, "POST", "/v1/ircd/stop", IRCDStopRequest{Instance: instance}, nil)
}
func (c *Client) RestartIRCD(ctx context.Context, instance, operation domain.ID) (IRCDStatus, error) {
	var out IRCDStatus
	err := c.callChannel(ctx, "POST", "/v1/ircd/restart", IRCDRestartRequest{Instance: instance, OperationID: operation}, &out)
	return out, err
}
func (c *Client) IRCDAccount(ctx context.Context, account string) (IRCDConnection, error) {
	var out IRCDConnection
	err := c.callChannel(ctx, "POST", "/v1/ircd/account", IRCDAccountRequest{Account: account}, &out)
	return out, err
}
func (c *Client) BindIRCD(ctx context.Context, in IRCDBindRequest) (ChannelStatus, error) {
	var out ChannelStatus
	err := c.callChannel(ctx, "POST", "/v1/ircd/bind", in, &out)
	return out, err
}
