package native

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/seanheiney/keelmux/internal/library"
	"golang.org/x/sys/unix"
)

type intent struct {
	Schema          int             `json:"schema"`
	Target          Target          `json:"target"`
	WorkspaceDevice uint64          `json:"workspace_device"`
	WorkspaceInode  uint64          `json:"workspace_inode"`
	PreviousDigest  string          `json:"previous_digest,omitempty"`
	Receipt         Receipt         `json:"receipt"`
	Operations      []FileOperation `json:"operations"`
	Cleanup         []string        `json:"cleanup"`
	Digest          string          `json:"digest"`
}

func sealIntent(i intent) string { i.Digest = ""; return hash(canonical(i)) }

type checkpoint func(string) error // Per-operation fault injection, never global.

type writerLock struct {
	t        *tree
	fd       int
	dev, ino uint64
}
type lockIdentity struct {
	Schema int    `json:"schema"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func (l *writerLock) release() { unix.Flock(l.fd, unix.LOCK_UN); unix.Close(l.fd) }
func (l *writerLock) check() error {
	if e := l.t.bound(); e != nil {
		return e
	}
	dev, ino, e := readLock(l.t)
	if e != nil {
		return e
	}
	if dev != l.dev || ino != l.ino {
		return ErrUnsafe
	}
	return nil
}
func readLock(t *tree) (uint64, uint64, error) {
	p := filepath.Join(ledgerDir, "lock")
	b, present, e := t.read(p, 4096)
	if e != nil {
		return 0, 0, e
	}
	if !present {
		return 0, 0, ErrUnsafe
	}
	var id lockIdentity
	if e = decode(b, &id); e != nil {
		return 0, 0, e
	}
	fd, name, e := t.parent(p, false)
	if e != nil {
		return 0, 0, e
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if e = unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); e != nil {
		return 0, 0, e
	}
	if id.Schema != 1 || id.Device != uint64(st.Dev) || id.Inode != uint64(st.Ino) || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		return 0, 0, ErrUnsafe
	}
	return id.Device, id.Inode, nil
}
func lock(ctx context.Context, t *tree) (*writerLock, error) {
	fd, name, e := t.parent(filepath.Join(ledgerDir, "lock"), true)
	if e != nil {
		return nil, e
	}
	defer unix.Close(fd)
	n, e := unix.Openat(fd, name, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	created := false
	if errors.Is(e, unix.ENOENT) {
		// Never recreate an arbitration inode once application state exists.
		var dup int
		dup, e = unix.Dup(fd)
		if e != nil {
			return nil, e
		}
		dir := os.NewFile(uintptr(dup), ledgerDir)
		var entries []os.DirEntry
		entries, e = dir.ReadDir(maxFiles + 1)
		dir.Close()
		if e != nil && e != io.EOF {
			return nil, e
		}
		if len(entries) > 0 {
			return nil, ErrUnsafe
		}
		n, e = unix.Openat(fd, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		created = e == nil
		if errors.Is(e, unix.EEXIST) {
			n, e = unix.Openat(fd, name, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		}
	}
	if e != nil {
		return nil, ErrUnsafe
	}
	var st unix.Stat_t
	if e = unix.Fstat(n, &st); e != nil {
		unix.Close(n)
		return nil, e
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		unix.Close(n)
		return nil, ErrUnsafe
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		e = unix.Flock(n, unix.LOCK_EX|unix.LOCK_NB)
		if e == nil {
			break
		}
		if !errors.Is(e, unix.EWOULDBLOCK) {
			unix.Close(n)
			return nil, e
		}
		select {
		case <-deadline.C:
			unix.Close(n)
			return nil, ErrBusy
		case <-ctx.Done():
			unix.Close(n)
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	l := &writerLock{t: t, fd: n, dev: uint64(st.Dev), ino: uint64(st.Ino)}
	if created {
		b := canonical(lockIdentity{1, l.dev, l.ino})
		if _, e = unix.Write(n, b); e == nil {
			e = unix.Fsync(n)
		}
		if e == nil {
			e = unix.Fsync(fd)
		}
		if e != nil {
			l.release()
			return nil, e
		}
	}
	if e = l.check(); e != nil {
		l.release()
		return nil, e
	}
	return l, nil
}

func guarded(l *writerLock, hook checkpoint) checkpoint {
	return func(stage string) error {
		if e := l.check(); e != nil {
			return e
		}
		if hook != nil {
			if e := hook(stage); e != nil {
				return e
			}
		}
		return l.check()
	}
}

// Apply re-resolves and regenerates a preview before creating state and again
// under the cooperating-writer lock. A pending local intent requires Recover.
func Apply(ctx context.Context, reg library.Registry, p Plan) (Receipt, error) {
	return apply(ctx, reg, p, nil)
}
func apply(ctx context.Context, reg library.Registry, p Plan, hook checkpoint) (Receipt, error) {
	var zero Receipt
	if p.Schema != 1 || !digestPattern.MatchString(p.Digest) || sealPlan(p) != p.Digest {
		return zero, ErrStale
	}
	if reg == nil {
		return zero, ErrUnsafe
	}
	r, e := reg.Resolve(ctx, p.ProfileID)
	if e != nil {
		return zero, e
	}
	fresh, e := Preview(ctx, reg, r, p.Target)
	if e != nil {
		return zero, e
	}
	if !bytes.Equal(canonical(fresh), canonical(p)) {
		return zero, ErrStale
	}
	if len(p.Collisions) > 0 {
		return zero, ErrCollision
	}
	mapped := false
	for _, op := range p.Files {
		if op.After.Present {
			mapped = true
			break
		}
	}
	if !mapped && len(p.Unmapped) > 0 {
		return zero, ErrUnsupported
	}
	if e = ctx.Err(); e != nil {
		return zero, e
	}
	t, e := openTree(p.Target.Workspace)
	if e != nil {
		return zero, e
	}
	defer t.close()
	unlock, e := lock(ctx, t)
	if e != nil {
		return zero, e
	}
	defer unlock.release()
	r, e = reg.Resolve(ctx, p.ProfileID)
	if e != nil {
		return zero, e
	}
	fresh, e = preview(ctx, reg, r, p.Target, t)
	if e != nil {
		return zero, e
	}
	if !bytes.Equal(canonical(fresh), canonical(p)) {
		return zero, ErrStale
	}
	old, e := currentReceipt(ctx, t, p.Target)
	if e != nil {
		return zero, e
	}
	if old != nil && old.Applied && old.ProfileID == p.ProfileID && old.ResolutionDigest == p.ResolutionDigest && old.ProfileRevision == p.ProfileRevision && len(p.Unmapped) == len(old.Unmapped) && equalFiles(old, p) {
		if e = unlock.check(); e != nil {
			return zero, e
		}
		return *old, nil
	}
	next := Receipt{Schema: 1, Target: p.Target, Revision: 1, ProfileID: p.ProfileID, ProfileRevision: p.ProfileRevision, ResolutionDigest: p.ResolutionDigest, PlanDigest: p.Digest, WorkspaceDevice: t.dev, WorkspaceInode: t.ino, LaunchArgs: p.LaunchArgs, Unmapped: p.Unmapped, Applied: true, Coverage: "complete_for_declared_resolution", Loaded: "unknown", Reload: "pending_or_unknown"}
	next.LockDevice = unlock.dev
	next.LockInode = unlock.ino
	if len(next.Unmapped) > 0 {
		next.Coverage = "partial_for_declared_resolution"
	}
	if old != nil {
		next.Revision = old.Revision + 1
		next.CreatedDirectories = append(next.CreatedDirectories, old.CreatedDirectories...)
	}
	created, e := missingParents(t, p.Files)
	if e != nil {
		return zero, e
	}
	next.CreatedDirectories = union(next.CreatedDirectories, created)
	for _, op := range p.Files {
		if op.After.Present {
			next.Files = append(next.Files, OwnedFile{op.Path, op.After.SHA256, len(op.Content)})
		}
	}
	next.Digest = sealReceipt(next)
	i := intent{Schema: 1, Target: p.Target, WorkspaceDevice: t.dev, WorkspaceInode: t.ino, PreviousDigest: p.ExpectedReceiptDigest, Receipt: next, Operations: p.Files}
	i.Digest = sealIntent(i)
	if e = commitIntent(ctx, t, i, guarded(unlock, hook)); e != nil {
		return zero, e
	}
	return next, nil
}
func equalFiles(r *Receipt, p Plan) bool {
	var files []OwnedFile
	for _, op := range p.Files {
		if op.After.Present {
			files = append(files, OwnedFile{op.Path, op.After.SHA256, len(op.Content)})
		}
	}
	return bytes.Equal(canonical(files), canonical(r.Files)) && bytes.Equal(canonical(p.LaunchArgs), canonical(r.LaunchArgs)) && bytes.Equal(canonical(p.Unmapped), canonical(r.Unmapped))
}
func union(a, b []string) []string {
	set := map[string]bool{}
	for _, s := range append(a, b...) {
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Remove only deletes unchanged owned files. It never restores or overwrites
// foreign native files, and it never changes a running harness.
func Remove(ctx context.Context, target Target, expectedReceiptDigest string) (Receipt, error) {
	var zero Receipt
	if !digestPattern.MatchString(expectedReceiptDigest) {
		return zero, ErrStale
	}
	s, e := Status(ctx, target)
	if e != nil {
		return zero, e
	}
	if s.Receipt == nil || s.Receipt.Digest != expectedReceiptDigest {
		return zero, ErrStale
	}
	if !s.Receipt.Applied {
		return *s.Receipt, nil
	}
	t, e := openTree(target.Workspace)
	if e != nil {
		return zero, e
	}
	defer t.close()
	unlock, e := lock(ctx, t)
	if e != nil {
		return zero, e
	}
	defer unlock.release()
	s, e = status(ctx, t, target)
	if e != nil {
		return zero, e
	}
	if s.Receipt == nil || s.Receipt.Digest != expectedReceiptDigest {
		return zero, ErrStale
	}
	old := s.Receipt
	next := *old
	next.Revision++
	next.Files = nil
	next.LaunchArgs = nil
	next.Applied = false
	next.CreatedDirectories = nil
	next.PlanDigest = hash([]byte("remove:" + old.Digest))
	next.Digest = sealReceipt(next)
	i := intent{Schema: 1, Target: target, WorkspaceDevice: t.dev, WorkspaceInode: t.ino, PreviousDigest: old.Digest, Receipt: next, Cleanup: old.CreatedDirectories}
	for _, f := range old.Files {
		i.Operations = append(i.Operations, FileOperation{Path: f.Path, Before: FileIdentity{true, f.SHA256}})
	}
	i.Digest = sealIntent(i)
	if e = commitIntent(ctx, t, i, guarded(unlock, nil)); e != nil {
		return zero, e
	}
	return next, nil
}
func commitIntent(ctx context.Context, t *tree, i intent, hook checkpoint) error {
	if e := validateIntent(t, i, i.Target); e != nil {
		return e
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if hook != nil {
		if e := hook("before:intent"); e != nil {
			return e
		}
	}
	b := canonical(i)
	if len(b) > maxStateBytes {
		return ErrUnsafe
	}
	if e := t.write(appPath(i.Target, "intent.json"), b, FileIdentity{}); e != nil {
		return e
	}
	if hook != nil {
		if e := hook("intent"); e != nil {
			return e
		}
	}
	return finishIntent(ctx, t, i, hook)
}
func operationAllowed(target Target, p string) bool {
	if !relative(p) {
		return false
	}
	if strings.HasPrefix(p, appPath(target, "generated")+"/") {
		return true
	}
	switch target.Harness {
	case "codex":
		return p == "AGENTS.md" || p == ".codex/config.toml" || strings.HasPrefix(p, ".agents/skills/")
	case "claude":
		return strings.HasPrefix(p, ".claude/skills/") || strings.HasPrefix(p, ".claude/rules/keelmux-"+target.ApplicationID+"-")
	case "gemini":
		return p == "GEMINI.md" || p == ".gemini/settings.json" || strings.HasPrefix(p, ".gemini/skills/")
	case "opencode":
		return p == "opencode.json" || strings.HasPrefix(p, ".opencode/skills/")
	}
	return false
}
func directoryAllowed(target Target, p string) bool {
	if !relative(p) || privatePath(p) {
		return false
	}
	var roots []string
	switch target.Harness {
	case "codex":
		roots = []string{".agents/skills", ".codex"}
	case "claude":
		roots = []string{".claude/skills", ".claude/rules"}
	case "gemini":
		roots = []string{".gemini/skills"}
	case "opencode":
		roots = []string{".opencode/skills"}
	}
	for _, root := range roots {
		if p == filepath.Dir(root) || p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}
func validateIntent(t *tree, i intent, target Target) error {
	if i.Schema != 1 || i.Target != target || i.WorkspaceDevice != t.dev || i.WorkspaceInode != t.ino || !digestPattern.MatchString(i.Digest) || sealIntent(i) != i.Digest || i.Receipt.Target != target || i.Receipt.Digest != sealReceipt(i.Receipt) || i.Receipt.WorkspaceDevice != t.dev || i.Receipt.WorkspaceInode != t.ino || len(i.Operations) > maxFiles || len(i.Cleanup) > maxFiles*8 {
		return ErrUnsafe
	}
	if e := validateReceipt(t, i.Receipt, target); e != nil {
		return e
	}
	seen := map[string]bool{}
	total := 0
	desired := map[string]string{}
	for _, op := range i.Operations {
		if !operationAllowed(target, op.Path) || seen[op.Path] || len(op.Content) > maxFileBytes || op.After != identity(op.Content, op.After.Present) || (!op.After.Present && len(op.Content) > 0) || (op.Before.Present && !digestPattern.MatchString(op.Before.SHA256)) || (!op.Before.Present && op.Before.SHA256 != "") {
			return ErrUnsafe
		}
		seen[op.Path] = true
		total += len(op.Content)
		if op.After.Present {
			desired[op.Path] = op.After.SHA256
		}
	}
	if total > maxTotalBytes || len(desired) != len(i.Receipt.Files) {
		return ErrUnsafe
	}
	for _, f := range i.Receipt.Files {
		if desired[f.Path] != f.SHA256 {
			return ErrUnsafe
		}
	}
	for _, p := range i.Cleanup {
		if !directoryAllowed(target, p) {
			return ErrUnsafe
		}
	}
	return nil
}
func finishIntent(ctx context.Context, t *tree, i intent, hook checkpoint) error {
	if e := validateIntent(t, i, i.Target); e != nil {
		return e
	}
	old, e := currentReceipt(ctx, t, i.Target)
	if e != nil {
		return e
	}
	if old != nil && old.Digest == i.Receipt.Digest { // Receipt commit succeeded, acknowledgement/intent cleanup did not.
		if drift, e := receiptDrift(ctx, t, old); e != nil {
			return e
		} else if len(drift) > 0 {
			return ErrDrift
		}
	} else {
		got := ""
		if old != nil {
			got = old.Digest
		}
		if got != i.PreviousDigest {
			return ErrStale
		}
		if len(i.Cleanup) > 0 && (old == nil || i.Receipt.Applied || !bytes.Equal(canonical(i.Cleanup), canonical(old.CreatedDirectories))) {
			return ErrUnsafe
		}
		// Validate every operation before another effect; unknown/edited content
		// blocks recovery rather than being repaired or overwritten.
		for _, op := range i.Operations {
			if hook != nil {
				if e = hook("before:file:" + op.Path); e != nil {
					return e
				}
			}
			got, e := t.owned(op.Path)
			if e != nil {
				return e
			}
			if got != op.Before && got != op.After {
				return fmt.Errorf("%w: pending path %s", ErrDrift, op.Path)
			}
		}
		for _, op := range i.Operations {
			if e = ctx.Err(); e != nil {
				return e
			}
			if hook != nil {
				if e = hook("before:effect:" + op.Path); e != nil {
					return e
				}
			}
			got, e := t.owned(op.Path)
			if e != nil {
				return e
			}
			if got == op.After {
				continue
			}
			if got != op.Before {
				return ErrDrift
			}
			if op.After.Present {
				e = t.write(op.Path, op.Content, op.Before)
			} else {
				e = t.erase(op.Path, op.Before)
			}
			if e != nil {
				return e
			}
			if hook != nil {
				if e = hook("file:" + op.Path); e != nil {
					return e
				}
			}
		}
		before, e := t.observe(appPath(i.Target, "receipt.json"))
		if e != nil {
			return e
		}
		if hook != nil {
			if e = hook("before:receipt"); e != nil {
				return e
			}
		}
		if e = t.write(appPath(i.Target, "receipt.json"), canonical(i.Receipt), before); e != nil {
			return e
		}
		if hook != nil {
			if e = hook("receipt"); e != nil {
				return e
			}
		}
	}
	// Owned empty native directories can be removed after all owned files. An
	// unrelated file makes RMDIR a no-op; it is preserved, never recursively removed.
	dirs := append([]string(nil), i.Cleanup...)
	sort.Slice(dirs, func(a, b int) bool { return len(dirs[a]) > len(dirs[b]) })
	for _, p := range dirs {
		if hook != nil {
			if e = hook("before:cleanup"); e != nil {
				return e
			}
		}
		fd, name, e := t.parent(p, false)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return e
		}
		var st unix.Stat_t
		e = unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(e, unix.ENOENT) {
			unix.Close(fd)
			continue
		}
		if e == nil && (st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0700 || st.Uid != uint32(os.Getuid())) {
			e = ErrUnsafe
		}
		if e == nil {
			e = unix.Unlinkat(fd, name, unix.AT_REMOVEDIR)
			if errors.Is(e, unix.ENOTEMPTY) || errors.Is(e, unix.EEXIST) {
				e = nil
			}
			if e == nil {
				e = unix.Fsync(fd)
			}
		}
		unix.Close(fd)
		if e != nil {
			return e
		}
	}
	got, e := t.observe(appPath(i.Target, "intent.json"))
	if e != nil {
		return e
	}
	if got != identity(canonical(i), true) {
		return ErrStale
	}
	if hook != nil {
		if e = hook("before:final"); e != nil {
			return e
		}
	}
	if e = t.erase(appPath(i.Target, "intent.json"), got); e != nil {
		return e
	}
	if hook != nil {
		return hook("after:final")
	}
	return nil
}

// Recover is an explicit completion of previously verified local file intent.
// It never resolves a fresh profile, starts a process, executes MCP or replays
// an unknown external effect. An edited preimage/afterimage refuses completion.
func Recover(ctx context.Context, target Target) (ApplicationStatus, error) {
	var out ApplicationStatus
	if e := targetOK(target); e != nil {
		return out, e
	}
	t, e := openTree(target.Workspace)
	if e != nil {
		return out, e
	}
	defer t.close()
	b, present, e := t.read(appPath(target, "intent.json"), maxStateBytes)
	if e != nil {
		return out, e
	}
	if !present {
		return status(ctx, t, target)
	}
	var i intent
	if e = decode(b, &i); e != nil {
		return out, e
	}
	if e = validateIntent(t, i, target); e != nil {
		return out, e
	}
	unlock, e := lock(ctx, t)
	if e != nil {
		return out, e
	}
	defer unlock.release()
	fresh, present, e := t.read(appPath(target, "intent.json"), maxStateBytes)
	if e != nil {
		return out, e
	}
	if !present {
		return status(ctx, t, target)
	}
	if !bytes.Equal(fresh, b) {
		return out, ErrStale
	}
	if unlock.dev != i.Receipt.LockDevice || unlock.ino != i.Receipt.LockInode {
		return out, ErrUnsafe
	}
	if e = finishIntent(ctx, t, i, guarded(unlock, nil)); e != nil {
		out.Pending = true
		return out, e
	}
	return status(ctx, t, target)
}
