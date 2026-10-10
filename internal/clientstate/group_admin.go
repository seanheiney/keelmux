package clientstate

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/domain"
)

func validGroupName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || len(name) > 128 || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ValidateGroupName validates a proposed display name after trimming its edges.
// It does not change navigation or create a persisted client place.
func ValidateGroupName(name string) error {
	if !validGroupName(strings.TrimSpace(name)) {
		return errors.New("group name must be 1 to 128 UTF-8 bytes without control characters")
	}
	return nil
}

// RenameGroup changes local navigation metadata only. Stable references,
// remembered places, addressed drafts and runtime authority remain intact.
func (s *State) RenameGroup(id domain.ID, name string) error {
	name = strings.TrimSpace(name)
	if err := ValidateGroupName(name); err != nil {
		return err
	}
	candidate := *s
	candidate.Groups = append([]Group(nil), s.Groups...)
	for i := range candidate.Groups {
		if candidate.Groups[i].ID == id {
			candidate.Groups[i].Name = name
			if err := candidate.Validate(); err != nil {
				return err
			}
			s.Groups = candidate.Groups
			return nil
		}
	}
	return errors.New("navigation group is missing")
}

// ReparentGroup validates the complete proposed hierarchy before replacing it.
// An empty parent makes a flat collection. Moving a collection never moves work.
func (s *State) ReparentGroup(id, parent domain.ID) error {
	if id == "" {
		return errors.New("All work cannot have a parent")
	}
	if parent != "" {
		if _, ok := s.Group(parent); !ok {
			return errors.New("parent group is missing")
		}
	}
	candidate := *s
	candidate.Groups = append([]Group(nil), s.Groups...)
	for i := range candidate.Groups {
		if candidate.Groups[i].ID == id {
			candidate.Groups[i].ParentID = parent
			if err := candidate.Validate(); err != nil {
				return err
			}
			s.Groups = candidate.Groups
			return nil
		}
	}
	return errors.New("navigation group is missing")
}

// RemoveReference is an idempotent exact-reference removal. It cannot stop,
// unlink or delete a session, and cannot erase a thread's addressed draft.
func (s *State) RemoveReference(id domain.ID, ref Reference) error {
	if err := validateReference(ref); err != nil {
		return err
	}
	for i := range s.Groups {
		if s.Groups[i].ID != id {
			continue
		}
		members := make([]Reference, 0, len(s.Groups[i].Members))
		for _, member := range s.Groups[i].Members {
			if member != ref {
				members = append(members, member)
			}
		}
		s.Groups[i].Members = members
		return nil
	}
	return errors.New("navigation group is missing")
}
