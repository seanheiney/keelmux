package usage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/seanheiney/keelmux/internal/config"
	_ "modernc.org/sqlite"
)

const MaxObservations = 10000
const MaxObservationBytes = 64 * 1024
const MaxAttemptObservations = 256
const schema = `CREATE TABLE usage_observations (source_id TEXT NOT NULL, observation_id TEXT NOT NULL, kind TEXT NOT NULL, canonical_key TEXT NOT NULL, sequence TEXT NOT NULL, digest TEXT NOT NULL, data BLOB NOT NULL, PRIMARY KEY(source_id,observation_id), UNIQUE(source_id,canonical_key,sequence))`

// Ledger has one cooperating host-local authority. Caller-provided grants are
// projection policy; neither this lock nor those grants isolate same-user code.
type Ledger struct {
	mu   sync.Mutex
	db   *sql.DB
	lock *os.File
}

func Open(path string) (*Ledger, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("usage path must be clean and absolute")
	}
	if err := config.EnsurePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	lock, err := privateFile(path + ".lock")
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("usage authority already open")
	}
	f, err := privateFile(path)
	if err != nil {
		lock.Close()
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	l := &Ledger{db: db, lock: lock}
	fail := func(e error) (*Ledger, error) { _ = l.Close(); return nil, e }
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fail(err)
	}
	if version > 1 {
		return fail(fmt.Errorf("unsupported usage schema version %d", version))
	}
	if _, err = db.Exec(`PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;`); err != nil {
		return fail(err)
	}
	if version == 0 {
		tx, e := db.Begin()
		if e != nil {
			return fail(e)
		}
		var count int
		e = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&count)
		if e == nil && count != 0 {
			e = ErrCorrupt
		}
		if e == nil {
			_, e = tx.Exec(schema)
		}
		if e == nil {
			_, e = tx.Exec(`PRAGMA user_version=1`)
		}
		if e == nil {
			e = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if e != nil {
			return fail(e)
		}
	}
	var actual string
	if err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='usage_observations'`).Scan(&actual); err != nil || actual != schema {
		return fail(ErrCorrupt)
	}
	if _, err = l.observations(context.Background()); err != nil {
		return fail(err)
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fail(err)
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return fail(err)
	}
	return l, nil
}
func privateFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0600 {
		f.Close()
		return nil, errors.New("usage files require owned private regular files (0600)")
	}
	return f, nil
}
func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var err error
	if l.db != nil {
		err = l.db.Close()
		l.db = nil
	}
	if l.lock != nil {
		e := l.lock.Close()
		if err == nil {
			err = e
		}
		l.lock = nil
	}
	return err
}
func canonical(o Observation) (string, string) {
	var v any
	var kind string
	if o.Attempt != nil {
		kind = "attempt"
		v = o.Attempt.Key
	}
	if o.Money != nil {
		kind = "money"
		v = []string{o.Money.Provider, o.Money.Namespace, o.Money.RecordID}
	}
	if o.Quota != nil {
		kind = "quota"
		v = []string{o.Quota.Provider, o.Quota.Namespace, o.Quota.BucketID, o.Quota.WindowID}
	}
	b, _ := json.Marshal(v)
	return kind, string(b)
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type IngestResult struct {
	Duplicate    bool   `json:"duplicate"`
	Kind         string `json:"kind"`
	CanonicalKey string `json:"canonical_key"`
}

// Ingest commits before returning. It rejects changed immutable observations;
// a new original-source sequence can enrich an existing canonical record.
func (l *Ledger) Ingest(ctx context.Context, o Observation) (IngestResult, error) {
	results, err := l.IngestBatch(ctx, []Observation{o})
	if err != nil {
		return IngestResult{}, err
	}
	return results[0], nil
}

// ingestPrepared uses the caller's single transaction. Preparation and conflict
// rules are shared by single-record ingestion and bounded atomic batch imports.
func ingestPrepared(ctx context.Context, tx *sql.Tx, p preparedObservation) (IngestResult, error) {
	o, data, kind, key := p.observation, p.data, p.kind, p.key
	result := IngestResult{Kind: kind, CanonicalKey: key}
	var old []byte
	err := tx.QueryRowContext(ctx, `SELECT data FROM usage_observations WHERE source_id=? AND observation_id=?`, o.Source.ID, o.ID).Scan(&old)
	if err == nil {
		if !bytes.Equal(data, old) {
			return result, ErrConflict
		}
		result.Duplicate = true
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var sourceData []byte
	err = tx.QueryRowContext(ctx, `SELECT data FROM usage_observations WHERE source_id=? LIMIT 1`, o.Source.ID).Scan(&sourceData)
	if err == nil {
		var previous Observation
		if json.Unmarshal(sourceData, &previous) != nil {
			return result, ErrCorrupt
		}
		if previous.Source.Kind != o.Source.Kind || previous.Source.Quality != o.Source.Quality {
			return result, ErrConflict
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM usage_observations`).Scan(&count); err != nil {
		return result, err
	}
	if count >= MaxObservations {
		return result, ErrCapacity
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM usage_observations WHERE kind=? AND canonical_key=?`, kind, key).Scan(&count); err != nil {
		return result, err
	}
	if count >= MaxAttemptObservations {
		return result, ErrCapacity
	}
	sequence := fmt.Sprint(o.Source.Sequence)
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM usage_observations WHERE source_id=? AND canonical_key=? AND sequence=?`, o.Source.ID, key, sequence).Scan(&exists); err != nil {
		return result, err
	}
	if exists != 0 {
		return result, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_observations VALUES(?,?,?,?,?,?,?)`, o.Source.ID, o.ID, kind, key, sequence, digest(data), data)
	if err != nil {
		return result, err
	}
	return result, nil
}

type observationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (l *Ledger) observations(ctx context.Context) ([]Observation, error) {
	return readObservations(ctx, l.db)
}
func readObservations(ctx context.Context, q observationQueryer) ([]Observation, error) {
	rows, err := q.QueryContext(ctx, `SELECT source_id,observation_id,kind,canonical_key,sequence,digest,data FROM usage_observations ORDER BY kind,canonical_key,source_id,observation_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Observation{}
	sourceIdentity := map[string]Source{}
	perKey := map[string]int{}
	for rows.Next() {
		var source, id, kind, key, sequence, sum string
		var data []byte
		if err = rows.Scan(&source, &id, &kind, &key, &sequence, &sum, &data); err != nil {
			return nil, err
		}
		if len(data) > MaxObservationBytes || len(out) >= MaxObservations || digest(data) != sum {
			return nil, ErrCorrupt
		}
		var o Observation
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&o) != nil || o.Validate() != nil {
			return nil, ErrCorrupt
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return nil, ErrCorrupt
		}
		k, c := canonical(o)
		canonicalData, _ := json.Marshal(normalized(o))
		if source != o.Source.ID || id != o.ID || kind != k || key != c || sequence != fmt.Sprint(o.Source.Sequence) || !bytes.Equal(data, canonicalData) {
			return nil, ErrCorrupt
		}
		if previous, ok := sourceIdentity[source]; ok && (previous.Kind != o.Source.Kind || previous.Quality != o.Source.Quality) {
			return nil, ErrCorrupt
		}
		sourceIdentity[source] = o.Source
		perKey[k+":"+c]++
		if perKey[k+":"+c] > MaxAttemptObservations {
			return nil, ErrCorrupt
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
