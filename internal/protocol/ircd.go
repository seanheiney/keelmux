package protocol

import (
	"context"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
	"github.com/seanheiney/keelmux/internal/ircdchannel"
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
	ProjectID   domain.ID `json:"project_id"`
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
	if err == nil && (!validIRCDConnection(out) || out.Account != "keelmux-bot") {
		err = errChannelResponse
	}
	if err != nil {
		return IRCDConnection{}, err
	}
	return out, nil
}
func (c *Client) IRCD(ctx context.Context) (IRCDStatus, error) {
	var out IRCDStatus
	err := c.callChannel(ctx, "GET", "/v1/ircd/status", nil, &out)
	if err == nil && !validIRCDStatus(out) {
		err = errChannelResponse
	}
	if err != nil {
		return IRCDStatus{}, err
	}
	return out, nil
}
func (c *Client) StartIRCD(ctx context.Context, id domain.ID) (IRCDStatus, error) {
	var out IRCDStatus
	err := c.callChannel(ctx, "POST", "/v1/ircd/start", IRCDStartRequest{OperationID: id}, &out)
	if err == nil && (!validIRCDStatus(out) || out.OperationID != id || !out.Alive || out.State != "running") {
		err = errChannelResponse
	}
	if err != nil {
		return IRCDStatus{}, err
	}
	return out, nil
}
func (c *Client) StopIRCD(ctx context.Context, instance domain.ID) error {
	return c.callChannel(ctx, "POST", "/v1/ircd/stop", IRCDStopRequest{Instance: instance}, nil)
}
func (c *Client) RestartIRCD(ctx context.Context, instance, operation domain.ID) (IRCDStatus, error) {
	var out IRCDStatus
	err := c.callChannel(ctx, "POST", "/v1/ircd/restart", IRCDRestartRequest{Instance: instance, OperationID: operation}, &out)
	if err == nil && (!validIRCDStatus(out) || out.OperationID != operation || out.Instance == instance || !out.Alive || out.State != "running") {
		err = errChannelResponse
	}
	if err != nil {
		return IRCDStatus{}, err
	}
	return out, nil
}
func (c *Client) IRCDAccount(ctx context.Context, account string) (IRCDConnection, error) {
	var out IRCDConnection
	err := c.callChannel(ctx, "POST", "/v1/ircd/account", IRCDAccountRequest{Account: account}, &out)
	if err == nil && (!validIRCDConnection(out) || out.Account != account) {
		err = errChannelResponse
	}
	if err != nil {
		return IRCDConnection{}, err
	}
	return out, nil
}
func (c *Client) BindIRCD(ctx context.Context, in IRCDBindRequest) (ChannelStatus, error) {
	var out ChannelStatus
	err := c.callChannel(ctx, "POST", "/v1/ircd/bind", in, &out)
	allow := append([]string{}, in.Allowlist...)
	sort.Strings(allow)
	channel, channelErr := ircdchannel.ChannelName(out.ThreadID)
	if err == nil && (out.ID != in.ID || out.SessionID != in.SessionID || out.Provider != channels.IRC || !validIRCDEndpoint(out.Endpoint, out.RealmID) || out.SelfID != "keelmux-bot" || channelErr != nil || out.ChannelID != channel || out.RootID != "" || out.SemanticDispatch || out.ActivationCutoff.IsZero() || !reflect.DeepEqual(out.Allowlist, allow)) {
		err = errChannelResponse
	}
	if err != nil {
		return ChannelStatus{}, err
	}
	return out, nil
}

func validIRCDIdentity(id domain.ID, kind string) bool {
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
func validIRCDOperationID(id domain.ID) bool {
	return validIRCDIdentity(id, "operation") || validIRCDIdentity(id, "op")
}
func validIRCDEndpoint(endpoint, realm string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "ircs" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Host != realm || u.String() != endpoint {
		return false
	}
	host, port, err := net.SplitHostPort(realm)
	n, e := strconv.Atoi(port)
	return err == nil && e == nil && host == "127.0.0.1" && n > 0 && n <= 65535 && strconv.Itoa(n) == port && net.JoinHostPort(host, port) == realm
}
func validIRCDTarget(target string) bool {
	switch target {
	case "darwin/arm64", "darwin/amd64", "linux/arm64", "linux/amd64":
		return true
	}
	return false
}
func validIRCDConnection(c IRCDConnection) bool {
	if !validIRCDIdentity(c.ServerID, "ircd") || !validIRCDEndpoint(c.Endpoint, c.RealmID) || !validIRCDAccountName(c.Account) || c.Version != ircd.Version || c.Source != ircd.SourceCommit || !validIRCDTarget(c.Target) {
		return false
	}
	for _, p := range []string{c.CAFile, c.CredentialRef} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return false
		}
	}
	return true
}

func validIRCDAccountName(s string) bool {
	if len(s) < 1 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func validIRCDStatus(s IRCDStatus) bool {
	if !validIRCDIdentity(s.ServerID, "ircd") || !validIRCDEndpoint(s.Endpoint, s.RealmID) || s.Account != "keelmux-bot" || s.Source != ircd.SourceCommit || !validIRCDTarget(s.Target) || s.ObservedAt.IsZero() {
		return false
	}
	if s.State == "initialized" {
		return !s.Alive && s.Instance == "" && s.OperationID == ""
	}
	if !validIRCDIdentity(s.Instance, "execution") || !validIRCDOperationID(s.OperationID) {
		return false
	}
	switch s.State {
	case "running":
		return s.Alive && s.Version == ircd.Version
	case "attempted", "stop_attempted", "uncertain", "stopped", "exited":
		return !s.Alive
	default:
		return false
	}
}
