package channels

import (
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxTextBytes   = 16 << 10
	MaxPageSize    = 100
	MaxCursorBytes = 4096
)

var ErrInvalid = errors.New("invalid channel contract")

func ValidID(s string) bool {
	return s != "" && len(s) <= 1024 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}

func ValidText(s string) bool {
	return len(s) <= MaxTextBytes && utf8.ValidString(s) && !strings.ContainsRune(s, 0) && strings.TrimSpace(s) != ""
}

func ValidCursor(s string) bool {
	return len(s) <= MaxCursorBytes && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// NormalizeBinding sorts a private copied allowlist; it neither resolves an
// endpoint nor invents verified realm, self, root or activation evidence.
func NormalizeBinding(b Binding) (Binding, error) {
	if b.Provider != Mattermost && b.Provider != Slack && b.Provider != IRC {
		return b, ErrInvalid
	}
	for _, id := range []string{string(b.ID), string(b.SessionID), string(b.ThreadID), string(b.WorkID), string(b.HostID), b.Endpoint, b.RealmID, b.SelfID, b.ChannelID, b.CredentialRef} {
		if !ValidID(id) {
			return b, ErrInvalid
		}
	}
	if b.ActivationCutoff.IsZero() || len(b.Allowlist) > 256 || (b.Provider == IRC && b.RootID != "") || (b.Provider != IRC && !ValidID(b.RootID)) {
		return b, ErrInvalid
	}
	b.Allowlist = append([]string{}, b.Allowlist...)
	sort.Strings(b.Allowlist)
	for n, id := range b.Allowlist {
		if !ValidID(id) || (n > 0 && b.Allowlist[n-1] == id) {
			return b, ErrInvalid
		}
	}
	b.ActivationCutoff = b.ActivationCutoff.UTC()
	return b, nil
}

func (e Evidence) Matches(b Binding) bool {
	return e.Provider == b.Provider && e.Endpoint == b.Endpoint && e.RealmID == b.RealmID && e.SelfID == b.SelfID && e.ChannelID == b.ChannelID && e.RootID == b.RootID && ValidID(e.Source) && !e.ObservedAt.IsZero()
}

func (m RemoteMessage) Matches(b Binding) bool {
	return ValidID(m.ID) && ValidID(m.ActorID) && m.RealmID == b.RealmID && m.ChannelID == b.ChannelID && m.RootID == b.RootID && !m.CreatedAt.IsZero() && ValidText(m.Text)
}

// Public status codes are finite; raw API errors belong only in private logs.
func ValidCode(s string) bool {
	switch s {
	case "", "remote_rejected", "transport_uncertain", "unconfirmed", "unsupported", "binding_disabled", "recovered_uncertain":
		return true
	}
	return false
}
