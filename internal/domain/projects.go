package domain

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Project is a durable home-owned identity. Names and workspace paths describe
// it; neither a local navigation group nor a native process creates membership.
type Project struct {
	ID        ID        `json:"id"`
	HostID    ID        `json:"host_id"`
	Name      string    `json:"name"`
	Workspace string    `json:"workspace"`
	CreatedAt time.Time `json:"created_at"`
}

func ValidObjectID(id ID, kind string) bool {
	s := string(id)
	if len(s) != len(kind)+33 || !strings.HasPrefix(s, kind+"_") {
		return false
	}
	for _, c := range s[len(kind)+1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (p Project) Validate() error {
	if !ValidObjectID(p.ID, "project") || !ValidObjectID(p.HostID, "host") ||
		p.Name == "" || p.Name != strings.TrimSpace(p.Name) || len(p.Name) > 128 ||
		!utf8.ValidString(p.Name) || !filepath.IsAbs(p.Workspace) ||
		filepath.Clean(p.Workspace) != p.Workspace || len(p.Workspace) > 4096 ||
		!utf8.ValidString(p.Workspace) || strings.ContainsAny(p.Workspace, "\x00\r\n") || p.CreatedAt.IsZero() {
		return errors.New("invalid project identity or configuration")
	}
	for _, c := range p.Name {
		if unicode.IsControl(c) {
			return errors.New("invalid project name")
		}
	}
	return nil
}
