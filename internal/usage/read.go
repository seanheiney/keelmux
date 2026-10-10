package usage

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
)

// ReadProjection never creates, chmods, migrates or obtains the writer lock.
// The bool reports whether a ledger exists. Query validation runs even when it
// does not exist, and that case returns an empty explicitly unknown projection.
func ReadProjection(ctx context.Context, path string, q Query) (Projection, bool, error) {
	if err := q.validate(); err != nil {
		return Projection{}, false, err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Projection{}, false, errors.New("usage path must be clean and absolute")
	}
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			p, e := projectObservations(nil, q)
			return p, false, e
		}
		return Projection{}, false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0700 {
		return Projection{}, false, errors.New("usage directory must be owned, real and private (0700)")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			p, e := projectObservations(nil, q)
			return p, false, e
		}
		return Projection{}, false, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return Projection{}, false, err
	}
	stat, ok = info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0600 {
		return Projection{}, false, errors.New("usage file must be owned, regular and private (0600)")
	}
	if info.Size() > int64(MaxObservations)*MaxObservationBytes {
		return Projection{}, false, ErrCapacity
	}
	u := url.URL{Scheme: "file", Path: path}
	v := url.Values{}
	v.Set("mode", "ro")
	u.RawQuery = v.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return Projection{}, true, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Projection{}, true, err
	}
	defer tx.Rollback()
	var version int
	var actual, journal string
	if err = tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != 1 {
		return Projection{}, true, ErrCorrupt
	}
	if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='usage_observations'`).Scan(&actual); err != nil || actual != schema {
		return Projection{}, true, ErrCorrupt
	}
	if err = tx.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "delete" {
		return Projection{}, true, ErrCorrupt
	}
	observations, err := readObservations(ctx, tx)
	if err != nil {
		return Projection{}, true, err
	}
	if err = tx.Commit(); err != nil {
		return Projection{}, true, err
	}
	p, err := projectObservations(observations, q)
	return p, true, err
}
