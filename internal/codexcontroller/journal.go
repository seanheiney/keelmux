package codexcontroller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"golang.org/x/sys/unix"
)

const maxJournalBytes = 4 << 20
const maxPrimaries = 1024
const journalVersion = 2

// Journal is a host-private lifecycle ledger, not writer authority. Its flock
// protects the file from concurrent controller processes; grants remain solely
// in the existing broker store. Open/Close never stop or respawn any process.
type Journal struct {
	mu        sync.Mutex
	dir       string
	directory *os.File
	lock      *os.File
	entries   map[domain.ID]entry
	poisoned  bool
}

// Journal is an opaque handle. Diagnostic logging must not reveal its ledger.
func (*Journal) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }
func (*Journal) String() string               { return "[private Codex journal]" }
func (*Journal) GoString() string             { return "[private Codex journal]" }

// Private disk representation: unlike Launch it is intentionally serializable
// only here. This ledger contains a local capability and must stay mode 0600.
type entry struct {
	Request                                                     StartRequest
	Incarnation                                                 domain.ID
	Endpoint, Binary, BinarySHA256, Home, CodexHome, Capability string
	HomeIdentity, CodexHomeIdentity                             directoryIdentity
	Process                                                     domain.RuntimeRef
	Evidence                                                    Evidence
	State                                                       State
	StopOperation                                               domain.ID
	Reason                                                      string
	UpdatedAt                                                   time.Time
	Stdio                                                       *stdioRecord `json:",omitempty"`
}
type disk struct {
	Version int
	Entries map[domain.ID]entry
}

func privateInfo(f *os.File, directory bool) error {
	i, err := f.Stat()
	if err != nil {
		return ErrStorage
	}
	s, ok := i.Sys().(*syscall.Stat_t)
	perm := os.FileMode(0600)
	if directory {
		perm = 0700
	}
	if !ok || s.Uid != uint32(os.Geteuid()) || i.Mode().Perm() != perm || i.IsDir() != directory || (!directory && (!i.Mode().IsRegular() || s.Nlink != 1)) {
		return ErrStorage
	}
	return nil
}

// Validate the descriptor we use, without following a link or waiting on a FIFO.
// The parent descriptor also keeps all journal IO in the directory we verified.
func privateFileAt(dir *os.File, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if err := privateInfo(f, false); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func OpenJournal(dir string) (*Journal, error) {
	if !cleanPath(dir) || len(filepath.Join(dir, "app-"+string(make([]byte, 32))+".sock")) > 100 {
		return nil, ErrInvalid
	}
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, ErrStorage
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrStorage
	}
	directory := os.NewFile(uintptr(fd), dir)
	if err := privateInfo(directory, true); err != nil {
		directory.Close()
		return nil, err
	}
	f, err := privateFileAt(directory, "controller.lock", unix.O_CREAT|unix.O_RDWR)
	if err != nil {
		directory.Close()
		return nil, ErrStorage
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		directory.Close()
		return nil, ErrConflict
	}
	j := &Journal{dir: dir, directory: directory, lock: f, entries: map[domain.ID]entry{}}
	r, err := privateFileAt(directory, "primaries.json", unix.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		j.Close()
		return nil, ErrStorage
	}
	b, err := io.ReadAll(io.LimitReader(r, maxJournalBytes+1))
	r.Close()
	var d disk
	if err != nil || len(b) > maxJournalBytes || json.Unmarshal(b, &d) != nil || d.Version != journalVersion || d.Entries == nil || len(d.Entries) > maxPrimaries {
		j.Close()
		return nil, ErrStorage
	}
	// This private ledger has a single canonical writer. Exact round-trip
	// equality rejects duplicate keys, case aliases, unknown fields, null resets,
	// and any representation that could discard or reinterpret a tombstone.
	canonical, err := json.Marshal(d)
	if err != nil || !bytes.Equal(b, canonical) {
		j.Close()
		return nil, ErrStorage
	}
	ops := map[domain.ID]bool{}
	for k, e := range d.Entries {
		if k != e.Request.Target.ExecutionID || !validEntry(e, dir) || ops[e.Request.OperationID] || (e.StopOperation != "" && ops[e.StopOperation]) || e.StopOperation == e.Request.OperationID {
			j.Close()
			return nil, ErrStorage
		}
		ops[e.Request.OperationID] = true
		if e.StopOperation != "" {
			ops[e.StopOperation] = true
		}
	}
	j.entries = d.Entries
	return j, nil
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.lock == nil {
		return nil
	}
	err := j.lock.Close()
	j.lock = nil
	if closeErr := j.directory.Close(); err == nil {
		err = closeErr
	}
	j.directory = nil
	return err
}
func (j *Journal) available() bool {
	if j.lock == nil || j.directory == nil || j.poisoned {
		return false
	}
	current, err := os.Lstat(j.dir)
	opened, statErr := j.directory.Stat()
	var lockPath, lockFD unix.Stat_t
	return err == nil && statErr == nil && os.SameFile(current, opened) && privateInfo(j.directory, true) == nil && privateInfo(j.lock, false) == nil &&
		unix.Fstatat(int(j.directory.Fd()), "controller.lock", &lockPath, unix.AT_SYMLINK_NOFOLLOW) == nil &&
		unix.Fstat(int(j.lock.Fd()), &lockFD) == nil && lockPath.Dev == lockFD.Dev && lockPath.Ino == lockFD.Ino && lockPath.Mode&unix.S_IFMT == unix.S_IFREG
}
func (j *Journal) operationUsed(op domain.ID) bool {
	for _, e := range j.entries {
		if e.Request.OperationID == op || e.StopOperation == op {
			return true
		}
	}
	return false
}

// save commits with fsync(file), atomic rename, fsync(parent). A failure poisons
// this handle because even a failed directory sync may have persisted the new
// record. No later effect is permitted until the journal is reopened/reconciled.
func (j *Journal) save(e entry) error {
	if !j.available() {
		return ErrStorage
	}
	next := make(map[domain.ID]entry, len(j.entries)+1)
	for k, v := range j.entries {
		next[k] = v
	}
	next[e.Request.Target.ExecutionID] = e
	if len(next) > maxPrimaries {
		return ErrStorage
	}
	b, err := json.Marshal(disk{Version: journalVersion, Entries: next})
	if err != nil || len(b) > maxJournalBytes {
		return ErrStorage
	}
	suffix, err := nonce(16)
	if err != nil {
		return ErrStorage
	}
	name := ".primary-" + suffix
	f, err := privateFileAt(j.directory, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY)
	if err != nil {
		return ErrStorage
	}
	defer unix.Unlinkat(int(j.directory.Fd()), name, 0)
	fail := func() error { f.Close(); j.poisoned = true; return ErrStorage }
	if err = f.Chmod(0600); err != nil {
		return fail()
	}
	if _, err = f.Write(b); err != nil {
		return fail()
	}
	if err = f.Sync(); err != nil {
		return fail()
	}
	if err = f.Close(); err != nil {
		return fail()
	}
	if err = unix.Renameat(int(j.directory.Fd()), name, int(j.directory.Fd()), "primaries.json"); err != nil {
		return fail()
	}
	if err = j.directory.Sync(); err != nil {
		return fail()
	}
	j.entries = next
	return nil
}

func (e entry) launch() Launch {
	return Launch{Request: e.Request, Incarnation: e.Incarnation, Endpoint: e.Endpoint, Binary: e.Binary, BinarySHA256: e.BinarySHA256, Home: e.Home, CodexHome: e.CodexHome, capability: e.Capability}
}
func (e entry) view() View {
	return View{Target: e.Request.Target, StartOperationID: e.Request.OperationID, StopOperationID: e.StopOperation, Incarnation: e.Incarnation, NativeThreadID: e.Request.NativeThreadID, State: e.State, Reason: e.Reason, UpdatedAt: e.UpdatedAt}
}
