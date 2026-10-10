package protocol

import (
	"context"
	"net/url"

	"github.com/seanheiney/keelmux/internal/domain"
)

type ProjectCreateRequest struct {
	ID        domain.ID `json:"project_id"`
	Name      string    `json:"name"`
	Workspace string    `json:"workspace"`
}

func (c *Client) Projects(ctx context.Context) ([]domain.Project, error) {
	var out []domain.Project
	if err := c.callChannel(ctx, "GET", "/v1/projects", nil, &out); err != nil {
		return nil, err
	}
	seen := map[domain.ID]bool{}
	var host domain.ID
	for _, p := range out {
		if p.Validate() != nil || seen[p.ID] || host != "" && p.HostID != host {
			return nil, errChannelResponse
		}
		seen[p.ID], host = true, p.HostID
	}
	return out, nil
}

func (c *Client) CreateProject(ctx context.Context, in ProjectCreateRequest) (domain.Project, error) {
	var out domain.Project
	if err := c.callChannel(ctx, "POST", "/v1/projects/create", in, &out); err != nil {
		return domain.Project{}, err
	}
	if out.Validate() != nil || out.ID != in.ID || out.Name != in.Name || out.Workspace != in.Workspace {
		return domain.Project{}, errChannelResponse
	}
	return out, nil
}

func (c *Client) Project(ctx context.Context, id domain.ID) (domain.Project, error) {
	if !domain.ValidObjectID(id, "project") {
		return domain.Project{}, errChannelResponse
	}
	var out domain.Project
	if err := c.callChannel(ctx, "GET", "/v1/projects/show?id="+url.QueryEscape(string(id)), nil, &out); err != nil {
		return domain.Project{}, err
	}
	if out.Validate() != nil || out.ID != id {
		return domain.Project{}, errChannelResponse
	}
	return out, nil
}
