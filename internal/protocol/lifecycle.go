package protocol

import (
	"context"
	"errors"

	"github.com/seanheiney/keelmux/internal/buildinfo"
)

type DaemonStatus struct {
	Instance string         `json:"instance"`
	PID      int            `json:"pid"`
	Protocol string         `json:"protocol"`
	Build    buildinfo.Info `json:"build"`
}

type DaemonStopRequest struct {
	Instance string `json:"instance"`
}

func (c *Client) Daemon(ctx context.Context) (DaemonStatus, error) {
	var status DaemonStatus
	err := c.Call(ctx, "GET", "/v1/daemon", nil, &status)
	if err == nil && (status.Protocol != Version || status.Instance == "" || status.PID <= 0) {
		err = errors.New("daemon lifecycle identity is unavailable or incompatible")
	}
	return status, err
}

func (c *Client) ShutdownDaemon(ctx context.Context, instance string) error {
	if instance == "" {
		return errors.New("daemon shutdown requires its exact live instance")
	}
	return c.Call(ctx, "POST", "/v1/daemon/stop", DaemonStopRequest{Instance: instance}, nil)
}
