// Package peerbridge exports an owner-selected read-only terminal scope over
// a fixed protocol. It never exposes the home's general control API.
package peerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/transport"
	"golang.org/x/sys/unix"
)

const Version = "keelmux-peer-bridge/1"
const (
	MaxSessions    = 256
	MaxOperations  = 256
	ControlLimit   = 64 << 10
	InventoryLimit = 1 << 20
	authorityLimit = 2 << 20
	ControlTimeout = 3 * time.Second
	IOTimeout      = 5 * time.Second
)

var (
	ErrUnavailable = errors.New("read bridge unavailable")
	ErrInvalid     = errors.New("invalid read bridge data")
	ErrDenied      = errors.New("read bridge scope denied")
	ErrConflict    = errors.New("read bridge identity or operation conflict")
	ErrUncertain   = errors.New("read bridge outcome uncertain; retain original operation for repair")
	ErrStorage     = errors.New("read bridge private persistence unconfirmed")
	ErrCapacity    = errors.New("read bridge bounded capacity reached")
)

type Selector struct {
	ScopeID   domain.ID `json:"scope_id"`
	HostID    domain.ID `json:"host_id"`
	Principal string    `json:"principal"`
}
type SessionActions struct {
	SessionID    domain.ID `json:"session_id"`
	Inventory    bool      `json:"inventory"`
	TerminalRead bool      `json:"terminal_read"`
}
type ReadPolicy struct {
	Version   string           `json:"version"`
	ScopeID   domain.ID        `json:"scope_id"`
	HostID    domain.ID        `json:"host_id"`
	Principal string           `json:"principal"`
	Socket    string           `json:"socket"`
	Revision  uint64           `json:"revision"`
	Enabled   bool             `json:"enabled"`
	Sessions  []SessionActions `json:"sessions"`
}
type PolicyStamp struct {
	ScopeID   domain.ID `json:"scope_id"`
	HostID    domain.ID `json:"host_id"`
	Principal string    `json:"principal"`
	Revision  uint64    `json:"revision"`
}

func (p ReadPolicy) stamp() PolicyStamp {
	return PolicyStamp{p.ScopeID, p.HostID, p.Principal, p.Revision}
}
func validText(s string, n int) bool {
	return s != "" && len(s) <= n && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}
func cleanPath(p string, n int) bool {
	return validText(p, n) && filepath.IsAbs(p) && filepath.Clean(p) == p
}
func validateSelector(s Selector) error {
	if !domain.ValidObjectID(s.ScopeID, "scope") || !domain.ValidObjectID(s.HostID, "host") || !validText(s.Principal, 128) || strings.TrimSpace(s.Principal) != s.Principal {
		return ErrInvalid
	}
	return nil
}
func validatePolicy(p ReadPolicy) error {
	if p.Version != Version || validateSelector(Selector{p.ScopeID, p.HostID, p.Principal}) != nil || !cleanPath(p.Socket, 100) || p.Revision == 0 || len(p.Sessions) > MaxSessions {
		return ErrInvalid
	}
	seen := map[domain.ID]bool{}
	for _, a := range p.Sessions {
		if !domain.ValidObjectID(a.SessionID, "session") || seen[a.SessionID] {
			return ErrInvalid
		}
		seen[a.SessionID] = true
	}
	return nil
}
func allowed(p ReadPolicy, id domain.ID, terminal bool) bool {
	if !p.Enabled {
		return false
	}
	for _, a := range p.Sessions {
		if a.SessionID == id {
			if terminal {
				return a.TerminalRead
			}
			return a.Inventory
		}
	}
	return false
}
func sameBinding(a, b ReadPolicy) bool {
	return a.Version == b.Version && a.ScopeID == b.ScopeID && a.HostID == b.HostID && a.Principal == b.Principal && a.Socket == b.Socket
}
func validateLease(r protocol.LeaseRequest, token bool) error {
	validOp := validOperationID(r.OperationID)
	if !domain.ValidObjectID(r.SessionID, "session") || !domain.ValidObjectID(r.ExecutionID, "execution") || !validOp || r.Generation == 0 || !validText(r.ClientID, 128) || !r.ReadOnly || (token && (!validText(r.Token, 256))) || (!token && r.Token != "") {
		return ErrInvalid
	}
	return nil
}
func validOperationID(id domain.ID) bool {
	s := string(id)
	i := strings.LastIndexByte(s, '_')
	if i < 1 || i > 32 || len(s)-i != 33 {
		return false
	}
	for _, c := range s[:i] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	for _, c := range s[i+1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func tokenFree(r protocol.LeaseRequest) protocol.LeaseRequest { r.Token = ""; return r }
func validateRequest(r transport.Request) error {
	if !domain.ValidObjectID(r.HostID, "host") || validateLease(r.Lease, false) != nil || r.Rows == 0 || r.Cols == 0 || r.Rows > 4096 || r.Cols > 4096 || len(r.Term) > 80 || strings.ContainsFunc(r.Term, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_+.-", c))
	}) {
		return ErrInvalid
	}
	return nil
}

// strict rejects duplicate and case-aliased authority fields as well as the
// ordinary unknown fields, invalid UTF-8, deep JSON and trailing values.
func strict(b []byte, limit int, v any) error {
	if len(b) > limit || !utf8.Valid(b) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if unique(d, 0) != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		return ErrInvalid
	}
	return nil
}
func unique(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			key, ok := k.(string)
			if !ok || key == "" || strings.ContainsFunc(key, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') }) {
				return ErrInvalid
			}
			if e != nil || !ok || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
			if unique(d, depth+1) != nil {
				return ErrInvalid
			}
		}
	case '[':
		for d.More() {
			if unique(d, depth+1) != nil {
				return ErrInvalid
			}
		}
	default:
		return ErrInvalid
	}
	end, e := d.Token()
	if e != nil || (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
		return ErrInvalid
	}
	return nil
}

type operation struct {
	Request          protocol.LeaseRequest `json:"request"`
	Revision         uint64                `json:"revision"`
	Lease            *domain.Lease         `json:"lease,omitempty"`
	OpenAttempted    bool                  `json:"open_attempted"`
	ReadyObserved    bool                  `json:"ready_observed"`
	ReleaseAttempted bool                  `json:"release_attempted"`
	Released         bool                  `json:"released"`
}
type authority struct {
	Version    string      `json:"version"`
	Policy     ReadPolicy  `json:"policy"`
	Operations []operation `json:"operations"`
}

func validateAuthority(a authority) error {
	if a.Version != Version || validatePolicy(a.Policy) != nil || len(a.Operations) > MaxOperations {
		return ErrInvalid
	}
	seen := map[domain.ID]bool{}
	for _, o := range a.Operations {
		r := o.Request
		if validateLease(r, false) != nil || seen[r.OperationID] || o.Revision == 0 || o.Revision > a.Policy.Revision || o.ReadyObserved && !o.OpenAttempted || o.Released && !o.ReleaseAttempted {
			return ErrInvalid
		}
		seen[r.OperationID] = true
		if o.Lease != nil {
			l := *o.Lease
			if l.SessionID != r.SessionID || l.ExecutionID != r.ExecutionID || l.Generation != r.Generation || l.OperationID != r.OperationID || l.ClientID != r.ClientID || !validText(l.Token, 256) {
				return ErrInvalid
			}
		} else if o.OpenAttempted || o.ReleaseAttempted {
			return ErrInvalid
		}
	}
	return nil
}

type authorityStore struct {
	mu          sync.Mutex
	dir, lock   *os.File
	path, name  string
	initial     ReadPolicy
	high        uint64
	policyBytes []byte
	known       map[domain.ID]operation
	poisoned    bool
	// Unexported fault seam for owned storage controls; nil uses File.Sync.
	syncHook func(*os.File) error
}

func owned(f *os.File, dir bool) bool {
	i, e := f.Stat()
	if e != nil {
		return false
	}
	s, ok := i.Sys().(*unix.Stat_t)
	if !ok { // syscall.Stat_t is the same platform ABI but has another Go type.
		var st unix.Stat_t
		if unix.Fstat(int(f.Fd()), &st) != nil {
			return false
		}
		s = &st
	}
	if s.Uid != uint32(os.Geteuid()) {
		return false
	}
	if dir {
		return i.IsDir() && i.Mode().Perm() == 0700
	}
	return i.Mode().IsRegular() && i.Mode().Perm() == 0600
}
func openDir(path string) (*os.File, error) {
	canonical, e := filepath.EvalSymlinks(path)
	if e != nil || canonical != path {
		return nil, ErrStorage
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, ErrStorage
	}
	f := os.NewFile(uintptr(fd), path)
	if !owned(f, true) {
		f.Close()
		return nil, ErrStorage
	}
	return f, nil
}
func openAt(dir *os.File, name string, flags int) (*os.File, error) {
	fd, e := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if e != nil {
		return nil, ErrStorage
	}
	f := os.NewFile(uintptr(fd), name)
	if !owned(f, false) {
		f.Close()
		return nil, ErrStorage
	}
	return f, nil
}
func samePathFile(dir *os.File, name string, f *os.File) bool {
	current, e := openAt(dir, name, unix.O_RDONLY)
	if e != nil {
		return false
	}
	defer current.Close()
	a, e := f.Stat()
	b, e2 := current.Stat()
	return e == nil && e2 == nil && os.SameFile(a, b)
}
func openStore(path string) (*authorityStore, error) {
	if !cleanPath(path, 4096) {
		return nil, ErrInvalid
	}
	d, e := openDir(filepath.Dir(path))
	if e != nil {
		return nil, e
	}
	s := &authorityStore{dir: d, path: path, name: filepath.Base(path)}
	s.lock, e = openAt(d, s.name+".lock", unix.O_RDWR)
	if e != nil {
		d.Close()
		return nil, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), ControlTimeout)
	defer cancel()
	e = s.access(ctx, false, func(a *authority) error { s.initial = a.Policy; return nil })
	if e != nil {
		s.close()
		return nil, e
	}
	return s, nil
}
func (s *authorityStore) close() { s.lock.Close(); s.dir.Close() }
func flock(ctx context.Context, f *os.File, mode int) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e := unix.Flock(int(f.Fd()), mode|unix.LOCK_NB)
		if e == nil {
			return nil
		}
		if e != unix.EWOULDBLOCK && e != unix.EAGAIN {
			return ErrStorage
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
func (s *authorityStore) access(ctx context.Context, write bool, fn func(*authority) error) (result error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if errors.Is(result, ErrStorage) {
			s.poisoned = true
		}
	}()
	if s.poisoned {
		return ErrStorage
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	current, e := openDir(filepath.Dir(s.path))
	if e != nil {
		return e
	}
	a, _ := s.dir.Stat()
	b, _ := current.Stat()
	current.Close()
	if !os.SameFile(a, b) || !samePathFile(s.dir, s.name+".lock", s.lock) {
		return ErrStorage
	}
	mode := unix.LOCK_SH
	if write {
		mode = unix.LOCK_EX
	}
	if e = flock(ctx, s.lock, mode); e != nil {
		return e
	}
	defer unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
	f, e := openAt(s.dir, s.name, unix.O_RDONLY)
	if e != nil {
		return e
	}
	data, e := io.ReadAll(io.LimitReader(f, authorityLimit+1))
	identityOK := samePathFile(s.dir, s.name, f)
	f.Close()
	if e != nil || !identityOK {
		return ErrStorage
	}
	var record authority
	if strict(data, authorityLimit, &record) != nil || validateAuthority(record) != nil {
		return ErrStorage
	}
	for id, prior := range s.known {
		o := findOperation(&record, id)
		if o == nil || o.Request != prior.Request || o.Revision != prior.Revision || prior.Lease != nil && (o.Lease == nil || *prior.Lease != *o.Lease) || prior.OpenAttempted && !o.OpenAttempted || prior.ReadyObserved && !o.ReadyObserved || prior.ReleaseAttempted && !o.ReleaseAttempted || prior.Released && !o.Released {
			return ErrStorage
		}
	}
	s.known = map[domain.ID]operation{}
	for _, o := range record.Operations {
		s.known[o.Request.OperationID] = o
	}
	pb, _ := json.Marshal(record.Policy)
	if s.high != 0 && (!sameBinding(s.initial, record.Policy) || record.Policy.Revision < s.high || record.Policy.Revision == s.high && !bytes.Equal(s.policyBytes, pb)) {
		s.poisoned = true
		return ErrConflict
	}
	s.high = record.Policy.Revision
	s.policyBytes = pb
	before, _ := json.Marshal(record)
	if e = fn(&record); e != nil {
		return e
	}
	if !write {
		return nil
	}
	if validateAuthority(record) != nil {
		return ErrInvalid
	}
	after, _ := json.Marshal(record)
	if bytes.Equal(before, after) {
		return nil
	}
	if e = s.save(ctx, record); e != nil {
		return e
	}
	s.high = record.Policy.Revision
	s.policyBytes, _ = json.Marshal(record.Policy)
	s.known = map[domain.ID]operation{}
	for _, o := range record.Operations {
		s.known[o.Request.OperationID] = o
	}
	return nil
}
func (s *authorityStore) syncFile(f *os.File) error {
	if s.syncHook != nil {
		return s.syncHook(f)
	}
	return f.Sync()
}

// confirm establishes current local storage durability without rewriting bytes.
// ReadPolicyFile does not invoke it. Serve invokes it before accepting effects.
func (s *authorityStore) confirm(ctx context.Context) error {
	return s.access(ctx, false, func(a *authority) error {
		f, e := openAt(s.dir, s.name, unix.O_RDONLY)
		if e != nil {
			return e
		}
		defer f.Close()
		before, e := io.ReadAll(io.LimitReader(f, authorityLimit+1))
		if e != nil || len(before) > authorityLimit || !samePathFile(s.dir, s.name, f) {
			return ErrStorage
		}
		var held authority
		if strict(before, authorityLimit, &held) != nil || !reflect.DeepEqual(held, *a) {
			return ErrStorage
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.syncFile(f) != nil || ctx.Err() != nil || s.syncFile(s.dir) != nil {
			return ErrStorage
		}
		if !samePathFile(s.dir, s.name, f) || !samePathFile(s.dir, s.name+".lock", s.lock) {
			return ErrStorage
		}
		if _, e = f.Seek(0, io.SeekStart); e != nil {
			return ErrStorage
		}
		after, e := io.ReadAll(io.LimitReader(f, authorityLimit+1))
		if e != nil || !bytes.Equal(before, after) || ctx.Err() != nil {
			return ErrStorage
		}
		return nil
	})
}
func (s *authorityStore) save(ctx context.Context, a authority) error {
	b, e := json.Marshal(a)
	if e != nil || len(b) > authorityLimit {
		return ErrCapacity
	}
	name := ".bridge-" + string(domain.NewID("write"))
	f, e := openAt(s.dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if e != nil {
		return e
	}
	defer unix.Unlinkat(int(s.dir.Fd()), name, 0)
	_, e = f.Write(b)
	if e == nil {
		e = s.syncFile(f)
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil || ctx.Err() != nil {
		return ErrStorage
	}
	if unix.Renameat(int(s.dir.Fd()), name, int(s.dir.Fd()), s.name) != nil || s.syncFile(s.dir) != nil {
		return ErrStorage
	}
	return nil
}
func ReadPolicyFile(path string) (ReadPolicy, error) {
	s, e := openStore(path)
	if e != nil {
		return ReadPolicy{}, e
	}
	defer s.close()
	return s.initial, nil
}
func ProvisionPolicy(ctx context.Context, path string, p ReadPolicy) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !cleanPath(path, 4096) || validatePolicy(p) != nil || p.Revision != 1 {
		return ErrInvalid
	}
	d, e := openDir(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	name := filepath.Base(path)
	created := []string{}
	ok := false
	defer func() {
		if !ok {
			for _, n := range created {
				unix.Unlinkat(int(d.Fd()), n, 0)
			}
		}
	}()
	for _, n := range []string{name, name + ".lock", name + ".serve"} {
		f, e := openAt(d, n, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY)
		if e != nil {
			return ErrStorage
		}
		created = append(created, n)
		if n == name {
			b, _ := json.Marshal(authority{Version: Version, Policy: p, Operations: []operation{}})
			_, e = f.Write(b)
		}
		if e == nil {
			e = f.Sync()
		}
		if ce := f.Close(); e == nil {
			e = ce
		}
		if e != nil {
			return ErrStorage
		}
	}
	if ctx.Err() != nil || d.Sync() != nil {
		return ErrStorage
	}
	ok = true
	return nil
}
func UpdatePolicy(ctx context.Context, path string, expected uint64, next ReadPolicy) error {
	ctx, cancel := context.WithTimeout(ctx, ControlTimeout)
	defer cancel()
	if validatePolicy(next) != nil {
		return ErrInvalid
	}
	s, e := openStore(path)
	if e != nil {
		return e
	}
	defer s.close()
	return s.access(ctx, true, func(a *authority) error {
		if expected != a.Policy.Revision || !sameBinding(a.Policy, next) {
			return ErrConflict
		}
		sort.Slice(next.Sessions, func(i, j int) bool { return next.Sessions[i].SessionID < next.Sessions[j].SessionID })
		old := a.Policy
		sort.Slice(old.Sessions, func(i, j int) bool { return old.Sessions[i].SessionID < old.Sessions[j].SessionID })
		next.Revision = old.Revision
		if reflect.DeepEqual(old, next) {
			return nil
		}
		if old.Revision == ^uint64(0) {
			return ErrConflict
		}
		next.Revision++
		a.Policy = next
		return nil
	})
}
