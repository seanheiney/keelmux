package federation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

type receiptStore struct {
	dir  string
	lock *os.File
}

type attachmentReceipt struct {
	Version   string       `json:"version"`
	Principal string       `json:"principal"`
	HostID    domain.ID    `json:"host_id"`
	Lease     domain.Lease `json:"lease"`
	ReadOnly  bool         `json:"read_only"`
	State     string       `json:"state,omitempty"`
}

// SetStateDirectory loads private principal/home acquisition receipts. Configure
// it before serving a gateway intended to recover saved TUI leases after restart.
// Confirmed receipts authorize exact release, including after navigation access
// is revoked. Pending receipts reserve the original principal and exact request
// for explicit home reconciliation. Neither form grants navigation permission.
func (g *Gateway) SetStateDirectory(dir string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.receipts != nil || len(g.acquisitions) != 0 || len(g.streams) != 0 || g.closed {
		return errors.New("configure private entry receipts before serving acquisitions")
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return errors.New("entry receipt directory must be an explicit clean absolute path")
	}
	if err := config.EnsurePrivateDirectory(dir); err != nil {
		return errors.New("private entry receipt directory unavailable")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return errors.New("entry receipt directory must be owned and private (0700)")
	}
	lock, err := privateReceiptFile(filepath.Join(dir, "entry.lock"))
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return errors.New("private entry receipt authority already active")
	}
	r := &receiptStore{dir, lock}
	loaded, err := r.load()
	if err != nil {
		_ = lock.Close()
		return err
	}
	attachments := make(map[domain.ID]attachment, len(loaded))
	acquisitions := make(map[domain.ID]acquisition, len(loaded))
	for _, record := range loaded {
		source, ok := g.peers[record.HostID].(StreamSource)
		if !ok {
			_ = lock.Close()
			return errors.New("saved entry receipt home requires its configured private stream source")
		}
		a := attachment{record.Principal, record.HostID, source, record.Lease, record.ReadOnly}
		if record.State != "pending" {
			attachments[a.lease.OperationID] = a
		}
		acquisitions[a.lease.OperationID] = acquisition{a.principal, a.host, requestFrom(a)}
	}
	g.attachments, g.acquisitions = attachments, acquisitions
	for id := range acquisitions {
		g.claimsDurable[id] = true
	}
	g.receipts = r
	return nil
}

// Close cancels this entry's streams and releases its own receipt lock. Native
// client detach and lease release remain confirmed by each home authority.
func (g *Gateway) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	for _, active := range g.streams {
		active.cancel()
	}
	if g.receipts != nil {
		return g.receipts.lock.Close()
	}
	return nil
}

func requestFrom(a attachment) protocol.LeaseRequest {
	l := a.lease
	return protocol.LeaseRequest{SessionID: l.SessionID, ExecutionID: l.ExecutionID, Generation: l.Generation, OperationID: l.OperationID, ClientID: l.ClientID, ReadOnly: a.readOnly}
}

func privateReceiptFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, errors.New("private entry receipt file unavailable")
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		_ = f.Close()
		return nil, errors.New("entry receipt file must be owned and private (0600)")
	}
	return f, nil
}

func receiptName(id domain.ID) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:]) + ".json"
}

func (r *receiptStore) load() ([]attachmentReceipt, error) {
	files, err := os.ReadDir(r.dir)
	if err != nil || len(files) > 10000 {
		return nil, errors.New("private entry receipts unavailable or exceed bounded experiment capacity")
	}
	var records []attachmentReceipt
	for _, entry := range files {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		f, err := privateReceiptFile(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil || info.Size() > 16384 {
			_ = f.Close()
			return nil, errors.New("private entry receipt exceeds bounded storage")
		}
		decoder := json.NewDecoder(io.LimitReader(f, 16385))
		decoder.DisallowUnknownFields()
		var record attachmentReceipt
		err = decoder.Decode(&record)
		if err == nil && !errors.Is(decoder.Decode(new(any)), io.EOF) {
			err = errors.New("multiple receipts")
		}
		_ = f.Close()
		l := record.Lease
		if err != nil || record.Version != protocol.Version || record.Principal == "" || record.HostID == "" || l.SessionID == "" || l.ExecutionID == "" || l.Generation == 0 || l.OperationID == "" || l.ClientID == "" || !validReceiptOutcome(record) || receiptName(l.OperationID) != entry.Name() {
			return nil, errors.New("invalid private entry acquisition receipt; repair required")
		}
		records = append(records, record)
	}
	return records, nil
}

func validReceiptOutcome(record attachmentReceipt) bool {
	switch record.State {
	case "pending":
		return record.Lease.Token == ""
	case "", "confirmed":
		return record.Lease.Token != "" // legacy confirmed receipt
	default:
		return false
	}
}

func (r *receiptStore) saveIntent(a acquisition) error {
	l := a.request
	return r.saveRecord(attachmentReceipt{Version: protocol.Version, Principal: a.principal, HostID: a.host,
		Lease: domain.Lease{SessionID: l.SessionID, ExecutionID: l.ExecutionID, Generation: l.Generation, OperationID: l.OperationID, ClientID: l.ClientID}, ReadOnly: l.ReadOnly, State: "pending"})
}

func (r *receiptStore) save(a attachment) error {
	return r.saveRecord(attachmentReceipt{Version: protocol.Version, Principal: a.principal, HostID: a.host, Lease: a.lease, ReadOnly: a.readOnly, State: "confirmed"})
}

func (r *receiptStore) saveRecord(record attachmentReceipt) error {
	data, err := json.Marshal(record)
	if err != nil || len(data) > 16384 {
		return errors.New("entry receipt exceeds bounded private storage")
	}
	f, err := os.CreateTemp(r.dir, ".receipt-")
	if err != nil {
		return errors.New("private entry receipt cannot be saved")
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil || os.Rename(f.Name(), filepath.Join(r.dir, receiptName(record.Lease.OperationID))) != nil {
		return errors.New("private entry receipt commit failed")
	}
	dir, err := os.Open(r.dir)
	if err != nil {
		return errors.New("private entry receipt directory sync unavailable")
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return errors.New("private entry receipt directory sync failed")
	}
	return nil
}
