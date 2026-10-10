// Package clientstate stores user-owned navigation, never runtime authority.
package clientstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
)

// Version 2 adds addressed channel drafts and immutable outbound intents. Old
// clients reject v2 rather than decoding unknown fields and erasing an intent.
const Version = 2

// The empty group ID means All work. References overlap without cloning work.
type Reference struct {
	Kind      string    `json:"kind"` // workspace, work, session, or thread
	ID        domain.ID `json:"id,omitempty"`
	Workspace string    `json:"workspace,omitempty"`
	HostID    domain.ID `json:"host_id,omitempty"` // optional workspace namespace; stable work/session/thread refs span targets
}

type Group struct {
	ID       domain.ID   `json:"id"`
	Name     string      `json:"name"`
	ParentID domain.ID   `json:"parent_id,omitempty"`
	Members  []Reference `json:"members,omitempty"`
}

type Place struct {
	SessionID domain.ID   `json:"session_id,omitempty"`
	ThreadID  domain.ID   `json:"thread_id,omitempty"`
	Filter    string      `json:"filter,omitempty"`
	Scroll    int         `json:"scroll,omitempty"`
	Expanded  []domain.ID `json:"expanded,omitempty"`
}

// Draft recipients are stable lanes, not execution or native conversation IDs.
type Draft struct {
	ThreadID    domain.ID `json:"thread_id"`
	RecipientID domain.ID `json:"recipient_id"`
	ExecutionID domain.ID `json:"execution_id"`
	Generation  uint64    `json:"generation"`
	Text        string    `json:"text"`
}

// Attachment retains intent across a lost response. Token records the exact
// cooperating lease; retaining it does not prove a native client is still alive.
type Attachment struct {
	SessionID   domain.ID `json:"session_id"`
	ExecutionID domain.ID `json:"execution_id"`
	Generation  uint64    `json:"generation"`
	OperationID domain.ID `json:"operation_id"`
	ClientID    string    `json:"client_id"`
	ReadOnly    bool      `json:"read_only"`
	Token       string    `json:"token,omitempty"`
}

type ChannelDraft struct {
	ThreadID  domain.ID `json:"thread_id"`
	BindingID domain.ID `json:"binding_id"`
	Text      string    `json:"text"`
}

// ChannelIntent is the exact local queue request. Editing/navigation must never
// retarget or replace it while acknowledgement remains unknown.
type ChannelIntent struct {
	MessageID domain.ID `json:"message_id"`
	BindingID domain.ID `json:"binding_id"`
	ThreadID  domain.ID `json:"thread_id"`
	Text      string    `json:"text"`
}

type State struct {
	Version        int                 `json:"version"`
	ClientID       string              `json:"client_id"`
	ActiveGroup    domain.ID           `json:"active_group,omitempty"`
	PreviousGroup  domain.ID           `json:"previous_group,omitempty"`
	Groups         []Group             `json:"groups,omitempty"`
	Places         map[domain.ID]Place `json:"places"`
	Drafts         []Draft             `json:"drafts,omitempty"`
	Pending        *Attachment         `json:"pending_attachment,omitempty"`
	ChannelDrafts  []ChannelDraft      `json:"channel_drafts,omitempty"`
	PendingChannel *ChannelIntent      `json:"pending_channel_send,omitempty"`
}

func New() State {
	return State{Version: Version, ClientID: string(domain.NewID("client")), Places: map[domain.ID]Place{}}
}

func Load(path string) (State, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	var s State
	d := json.NewDecoder(io.LimitReader(f, 4<<20))
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		return State{}, fmt.Errorf("read client place: %w", err)
	}
	var trailing any
	if err := d.Decode(&trailing); !errors.Is(err, io.EOF) {
		return State{}, errors.New("client place has trailing or oversized data")
	}
	if err := distinctStateKeys(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return State{}, fmt.Errorf("read client place: %w", err)
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return State{}, fmt.Errorf("read client place: %w", err)
	}
	if s.Version == 1 {
		// Only the original schema can migrate. A v1 file carrying new fields
		// is ambiguous and must not be silently promoted or reset.
		if len(s.ChannelDrafts) != 0 || s.PendingChannel != nil {
			return State{}, errors.New("version 1 place contains channel state; repair required")
		}
		s.Version = Version
	}
	if err := s.Validate(); err != nil {
		return State{}, err
	}
	if s.Places == nil {
		s.Places = map[domain.ID]Place{}
	}
	return s, nil
}

func (s State) Validate() error {
	if s.Version != Version {
		return fmt.Errorf("unsupported client place version %d", s.Version)
	}
	if s.ClientID == "" {
		return errors.New("client place is missing stable client identity")
	}
	seen := map[domain.ID]bool{}
	for _, g := range s.Groups {
		if g.ID == "" || seen[g.ID] || strings.TrimSpace(g.Name) == "" {
			return errors.New("invalid or duplicate navigation group")
		}
		seen[g.ID] = true
		for _, r := range g.Members {
			if err := validateReference(r); err != nil {
				return err
			}
		}
	}
	for _, g := range s.Groups {
		visited := map[domain.ID]bool{g.ID: true}
		for p := g.ParentID; p != ""; {
			if visited[p] || !seen[p] {
				return errors.New("navigation group parent is missing or cyclic")
			}
			visited[p] = true
			parent, _ := s.Group(p)
			p = parent.ParentID
		}
	}
	if s.ActiveGroup != "" && !seen[s.ActiveGroup] {
		return errors.New("active navigation group is missing")
	}
	if s.Pending != nil {
		p := s.Pending
		if p.SessionID == "" || p.ExecutionID == "" || p.Generation == 0 || p.OperationID == "" || p.ClientID != s.ClientID {
			return errors.New("invalid pending attachment identity")
		}
	}
	for _, d := range s.Drafts {
		if d.ThreadID == "" || d.RecipientID == "" {
			return errors.New("draft is missing thread or recipient")
		}
	}
	seenDrafts := map[[2]domain.ID]bool{}
	for _, d := range s.ChannelDrafts {
		key := [2]domain.ID{d.ThreadID, d.BindingID}
		if !validChannelDraft(d) || seenDrafts[key] {
			return errors.New("invalid or duplicate addressed channel draft")
		}
		seenDrafts[key] = true
	}
	if p := s.PendingChannel; p != nil {
		if !channels.ValidID(string(p.MessageID)) || !channels.ValidID(string(p.BindingID)) || !channels.ValidID(string(p.ThreadID)) || !channels.ValidText(p.Text) {
			return errors.New("invalid immutable channel send intent")
		}
		d, ok := s.ChannelDraft(p.ThreadID, p.BindingID)
		if !ok || d.Text != p.Text {
			return errors.New("pending channel intent differs from its frozen draft")
		}
	}
	return nil
}

// Save atomically replaces a private file. A failed write leaves the old file
// intact; ownership acquisition must stop if saving its intent fails.
func Save(path string, s State) error {
	if err := s.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if len(b)+1 > 4<<20 {
		return errors.New("client place exceeds the 4 MiB local state limit")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".place-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s State) Group(id domain.ID) (Group, bool) {
	for _, g := range s.Groups {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

func (s *State) SwitchGroup(id domain.ID) error {
	if id != "" {
		if _, ok := s.Group(id); !ok {
			return errors.New("navigation group is missing")
		}
	}
	if id != s.ActiveGroup {
		s.PreviousGroup, s.ActiveGroup = s.ActiveGroup, id
	}
	return nil
}

func (s *State) AddGroup(name string, parent domain.ID) (domain.ID, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("name the group")
	}
	if parent != "" {
		if _, ok := s.Group(parent); !ok {
			return "", errors.New("parent group is missing")
		}
	}
	id := domain.NewID("group")
	s.Groups = append(s.Groups, Group{ID: id, Name: name, ParentID: parent})
	return id, nil
}

func (s *State) AddReference(id domain.ID, ref Reference) error {
	if err := validateReference(ref); err != nil {
		return err
	}
	for i := range s.Groups {
		if s.Groups[i].ID != id {
			continue
		}
		for _, r := range s.Groups[i].Members {
			if r == ref {
				return nil
			}
		}
		s.Groups[i].Members = append(s.Groups[i].Members, ref)
		return nil
	}
	return errors.New("select a named group first")
}

func validateReference(r Reference) error {
	switch r.Kind {
	case "workspace":
		if !filepath.IsAbs(r.Workspace) {
			return errors.New("group workspace must be absolute")
		}
	case "work", "session", "thread":
		if r.ID == "" {
			return errors.New("group reference is missing identity")
		}
	default:
		return errors.New("unknown group reference kind")
	}
	return nil
}

// DeleteGroup reparents child collections; it cannot access the daemon.
func (s *State) DeleteGroup(id domain.ID) error {
	g, ok := s.Group(id)
	if !ok {
		return errors.New("navigation group is missing")
	}
	for i := range s.Groups {
		if s.Groups[i].ParentID == id {
			s.Groups[i].ParentID = g.ParentID
		}
	}
	for i := range s.Groups {
		if s.Groups[i].ID == id {
			s.Groups = append(s.Groups[:i], s.Groups[i+1:]...)
			break
		}
	}
	delete(s.Places, id)
	if s.ActiveGroup == id {
		s.ActiveGroup = ""
	}
	if s.PreviousGroup == id {
		s.PreviousGroup = ""
	}
	return nil
}

func (s State) Contains(groupID domain.ID, session domain.Session) bool {
	if groupID == "" {
		return true
	}
	for _, g := range s.Groups {
		inside := g.ID == groupID
		for p := g.ParentID; !inside && p != ""; {
			if p == groupID {
				inside = true
				break
			}
			parent, ok := s.Group(p)
			if !ok {
				break
			}
			p = parent.ParentID
		}
		if !inside {
			continue
		}
		for _, r := range g.Members {
			switch r.Kind {
			case "workspace":
				if r.Workspace == session.Workspace && (r.HostID == "" || r.HostID == session.HostID) {
					return true
				}
			case "work":
				if r.ID == session.WorkID {
					return true
				}
			case "session":
				if r.ID == session.ID {
					return true
				}
			case "thread":
				if r.ID == session.ThreadID {
					return true
				}
			}
		}
	}
	return false
}

func (s State) Draft(thread, recipient domain.ID) (Draft, bool) {
	for _, d := range s.Drafts {
		if d.ThreadID == thread && d.RecipientID == recipient {
			return d, true
		}
	}
	return Draft{}, false
}

func (s *State) SetDraft(d Draft) {
	for i, old := range s.Drafts {
		if old.ThreadID == d.ThreadID && old.RecipientID == d.RecipientID {
			s.Drafts[i] = d
			return
		}
	}
	s.Drafts = append(s.Drafts, d)
}

func (s State) ChannelDraft(thread, binding domain.ID) (ChannelDraft, bool) {
	for _, d := range s.ChannelDrafts {
		if d.ThreadID == thread && d.BindingID == binding {
			return d, true
		}
	}
	return ChannelDraft{}, false
}

func (s *State) SetChannelDraft(d ChannelDraft) error {
	if !validChannelDraft(d) {
		return errors.New("invalid addressed channel draft")
	}
	if p := s.PendingChannel; p != nil && p.ThreadID == d.ThreadID && p.BindingID == d.BindingID && p.Text != d.Text {
		return errors.New("channel draft is frozen until its exact intent is acknowledged")
	}
	for i, old := range s.ChannelDrafts {
		if old.ThreadID == d.ThreadID && old.BindingID == d.BindingID {
			s.ChannelDrafts[i] = d
			return nil
		}
	}
	s.ChannelDrafts = append(s.ChannelDrafts, d)
	return nil
}

func validChannelDraft(d ChannelDraft) bool {
	return channels.ValidID(string(d.ThreadID)) && channels.ValidID(string(d.BindingID)) && len(d.Text) <= channels.MaxTextBytes && utf8.ValidString(d.Text) && !strings.ContainsRune(d.Text, 0)
}
