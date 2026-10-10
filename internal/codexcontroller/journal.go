package codexcontroller

import (
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

// Journal is a host-private lifecycle ledger, not writer authority. Its flock
// protects the file from concurrent controller processes; grants remain solely
// in the existing broker store. Open/Close never stop or respawn any process.
type Journal struct {
	mu       sync.Mutex
	dir      string
	lock     *os.File
	entries  map[domain.ID]entry
	poisoned bool
}

// Private disk representation: unlike Launch it is intentionally serializable
// only here. This ledger contains a local capability and must stay mode 0600.
type entry struct {
	Request                                                     StartRequest
	Incarnation                                                 domain.ID
	Endpoint, Binary, BinarySHA256, Home, CodexHome, Capability string
	Process                                                     domain.RuntimeRef
	Evidence                                                    Evidence
	State                                                       State
	StopOperation                                               domain.ID
	Reason                                                      string
	UpdatedAt                                                   time.Time
}
type disk struct {
	Version int
	Entries map[domain.ID]entry
}

func privateInfo(path string, directory bool) error {
	i, err := os.Lstat(path)
	if err != nil {
		return ErrStorage
	}
	s, ok := i.Sys().(*syscall.Stat_t)
	perm := os.FileMode(0600)
	if directory {
		perm = 0700
	}
	if !ok || s.Uid != uint32(os.Geteuid()) || i.Mode().Perm() != perm || i.IsDir() != directory || (!directory && !i.Mode().IsRegular()) {
		return ErrStorage
	}
	return nil
}

func OpenJournal(dir string) (*Journal, error) {
	if !cleanPath(dir) || len(filepath.Join(dir, "app-"+string(make([]byte, 32))+".sock")) > 100 {
		return nil, ErrInvalid
	}
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, ErrStorage
	}
	if err := privateInfo(dir, true); err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(dir, "controller.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, ErrStorage
	}
	f := os.NewFile(uintptr(fd), "controller.lock")
	if err := privateInfo(filepath.Join(dir, "controller.lock"), false); err != nil {
		f.Close()
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrConflict
	}
	j := &Journal{dir: dir, lock: f, entries: map[domain.ID]entry{}}
	path := filepath.Join(dir, "primaries.json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err := privateInfo(path, false); err != nil {
		j.Close()
		return nil, err
	}
	r, err := os.Open(path)
	if err != nil {
		j.Close()
		return nil, ErrStorage
	}
	b, err := io.ReadAll(io.LimitReader(r, maxJournalBytes+1))
	r.Close()
	var d disk
	if err != nil || len(b) > maxJournalBytes || json.Unmarshal(b, &d) != nil || d.Version != 1 || d.Entries == nil || len(d.Entries) > maxPrimaries {
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
	return err
}
func (j *Journal) available() bool { return j.lock != nil && !j.poisoned }
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
	b, err := json.Marshal(disk{Version: 1, Entries: next})
	if err != nil || len(b) > maxJournalBytes {
		return ErrStorage
	}
	f, err := os.CreateTemp(j.dir, ".primary-")
	if err != nil {
		return ErrStorage
	}
	name := f.Name()
	defer os.Remove(name)
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
	if err = os.Rename(name, filepath.Join(j.dir, "primaries.json")); err != nil {
		return fail()
	}
	d, err := os.Open(j.dir)
	if err != nil {
		return fail()
	}
	err = d.Sync()
	closeErr := d.Close()
	if err != nil || closeErr != nil {
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
