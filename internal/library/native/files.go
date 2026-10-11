package native

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	maxFiles      = 512
	maxFileBytes  = 1 << 20
	maxTotalBytes = 16 << 20
	maxStateBytes = 64 << 20
	ledgerDir     = ".keelmux-native"
)

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func hash(b []byte) string         { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }
func canonical(v any) []byte       { b, _ := json.Marshal(v); return b }
func sealPlan(p Plan) string       { p.Digest = ""; return hash(canonical(p)) }
func sealReceipt(r Receipt) string { r.Digest = ""; return hash(canonical(r)) }
func identity(b []byte, present bool) FileIdentity {
	if !present {
		return FileIdentity{}
	}
	return FileIdentity{Present: true, SHA256: hash(b)}
}
func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }
func relative(s string) bool {
	return validText(s) && s != "." && s != "" && len(s) <= 4096 && len(strings.Split(s, "/")) <= 32 && !filepath.IsAbs(s) && filepath.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../") && !strings.Contains(s, "\\")
}
func targetOK(t Target) error {
	if !identifier.MatchString(t.ApplicationID) || !validText(t.Workspace) || !filepath.IsAbs(t.Workspace) || filepath.Clean(t.Workspace) != t.Workspace || t.Workspace == "/" {
		return ErrUnsafe
	}
	for _, r := range t.Workspace {
		if r < 32 || r == 127 {
			return ErrUnsafe
		}
	}
	switch t.Harness {
	case "codex", "claude", "gemini", "grok", "opencode":
	default:
		return ErrUnsupported
	}
	return nil
}
func appPath(t Target, name string) string { return filepath.Join(ledgerDir, t.ApplicationID, name) }

// Every component is opened relative to an owned directory fd with NOFOLLOW.
// This protects against symlink retargeting, not malicious same-user writers.
type tree struct {
	f    *os.File
	dev  uint64
	ino  uint64
	path string
}

func openTree(path string) (*tree, error) {
	if !filepath.IsAbs(path) || !validText(path) || filepath.Clean(path) != path {
		return nil, ErrUnsafe
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, fmt.Errorf("%w: directory component", ErrUnsafe)
		}
		fd = next
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if st.Uid != uint32(os.Getuid()) || st.Mode&0022 != 0 || st.Mode&07000 != 0 {
		unix.Close(fd)
		return nil, ErrUnsafe
	}
	return &tree{os.NewFile(uintptr(fd), path), uint64(st.Dev), uint64(st.Ino), path}, nil
}
func (t *tree) close() { t.f.Close() }
func (t *tree) bound() error {
	n, err := openTree(t.path)
	if err != nil {
		return err
	}
	defer n.close()
	if n.dev != t.dev || n.ino != t.ino {
		return ErrUnsafe
	}
	return nil
}
func privatePath(p string) bool { return p == ledgerDir || strings.HasPrefix(p, ledgerDir+"/") }
func directoryOK(fd int, private bool) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Uid != uint32(os.Getuid()) || st.Mode&0022 != 0 || st.Mode&07000 != 0 || (private && st.Mode&0777 != 0700) {
		return ErrUnsafe
	}
	return nil
}
func (t *tree) parent(p string, create bool) (int, string, error) {
	if !relative(p) {
		return -1, "", ErrUnsafe
	}
	fd, err := unix.Dup(int(t.f.Fd()))
	if err != nil {
		return -1, "", err
	}
	parts := strings.Split(p, "/")
	prefix := ""
	for _, part := range parts[:len(parts)-1] {
		prefix = filepath.Join(prefix, part)
		n, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(e, unix.ENOENT) && create {
			if e = unix.Mkdirat(fd, part, 0700); e == nil || errors.Is(e, unix.EEXIST) {
				e = unix.Fsync(fd)
			}
			if e == nil {
				n, e = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		unix.Close(fd)
		if e != nil {
			if errors.Is(e, unix.ENOENT) {
				return -1, "", os.ErrNotExist
			}
			return -1, "", ErrUnsafe
		}
		fd = n
		if e = directoryOK(fd, privatePath(prefix)); e != nil {
			unix.Close(fd)
			return -1, "", e
		}
	}
	return fd, parts[len(parts)-1], nil
}
func (t *tree) read(p string, limit int) ([]byte, bool, error) {
	fd, name, err := t.parent(p, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer unix.Close(fd)
	f, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, ErrUnsafe
	}
	file := os.NewFile(uintptr(f), name)
	defer file.Close()
	var st unix.Stat_t
	if err = unix.Fstat(f, &st); err != nil {
		return nil, false, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&07000 != 0 || st.Mode&0022 != 0 || st.Nlink != 1 || st.Size > int64(limit) || (privatePath(p) && st.Mode&0777 != 0600) {
		return nil, false, ErrUnsafe
	}
	b, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	if len(b) > limit {
		return nil, false, ErrUnsafe
	}
	var after, entry unix.Stat_t
	if unix.Fstat(f, &after) != nil || unix.Fstatat(fd, name, &entry, unix.AT_SYMLINK_NOFOLLOW) != nil || after.Dev != st.Dev || after.Ino != st.Ino || entry.Dev != st.Dev || entry.Ino != st.Ino || after.Mode != st.Mode || after.Uid != st.Uid || after.Nlink != st.Nlink || after.Size != st.Size || after.Mtim != st.Mtim || after.Ctim != st.Ctim {
		return nil, false, ErrUnsafe
	}
	return b, true, nil
}
func (t *tree) observe(p string) (FileIdentity, error) {
	limit := maxFileBytes
	if strings.HasSuffix(p, "/intent.json") || strings.HasSuffix(p, "/receipt.json") {
		limit = maxStateBytes
	}
	b, ok, e := t.read(p, limit)
	return identity(b, ok), e
}
func (t *tree) owned(p string) (FileIdentity, error) {
	got, e := t.observe(p)
	if e != nil || !got.Present {
		return got, e
	}
	fd, name, e := t.parent(p, false)
	if e != nil {
		return got, e
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if e = unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); e != nil {
		return got, e
	}
	if st.Mode&0777 != 0600 {
		return got, ErrUnsafe
	}
	return got, nil
}

// inspectEntry also catches empty skill directories: those are foreign native
// discovery roots, even if the desired SKILL.md file is absent.
func (t *tree) inspectEntry(p string) (bool, error) {
	fd, name, e := t.parent(p, false)
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	e = unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(e, unix.ENOENT) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if st.Uid != uint32(os.Getuid()) || st.Mode&07000 != 0 || st.Mode&0022 != 0 || (st.Mode&unix.S_IFMT != unix.S_IFREG && st.Mode&unix.S_IFMT != unix.S_IFDIR) {
		return false, ErrUnsafe
	}
	return true, nil
}
func (t *tree) write(p string, b []byte, before FileIdentity) error {
	if err := t.bound(); err != nil {
		return err
	}
	fd, name, e := t.parent(p, true)
	if e != nil {
		return e
	}
	defer unix.Close(fd)
	current, e := t.observe(p)
	if e != nil {
		return e
	}
	if current != before {
		return ErrDrift
	}
	// Exclusive temporary file, fsync, atomic rename, directory fsync. The
	// ledger serializes cooperating writers; unrelated same-user races are not
	// represented as a security boundary.
	var temp string
	var out int
	for i := 0; i < 8; i++ {
		random := make([]byte, 16)
		if _, e = io.ReadFull(rand.Reader, random); e != nil {
			return e
		}
		temp = ".keelmux-write-" + hex.EncodeToString(random)
		out, e = unix.Openat(fd, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if !errors.Is(e, unix.EEXIST) {
			break
		}
	}
	if e != nil {
		return e
	}
	defer unix.Unlinkat(fd, temp, 0)
	f := os.NewFile(uintptr(out), temp)
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	current, e = t.observe(p)
	if e != nil {
		return e
	}
	if current != before {
		return ErrDrift
	}
	if e = unix.Renameat(fd, temp, fd, name); e != nil {
		return e
	}
	return unix.Fsync(fd)
}
func (t *tree) erase(p string, before FileIdentity) error {
	if err := t.bound(); err != nil {
		return err
	}
	got, e := t.observe(p)
	if e != nil {
		return e
	}
	if got != before {
		return ErrDrift
	}
	if !got.Present {
		return nil
	}
	fd, name, e := t.parent(p, false)
	if e != nil {
		return e
	}
	defer unix.Close(fd)
	if e = unix.Unlinkat(fd, name, 0); e != nil {
		return e
	}
	return unix.Fsync(fd)
}
func decode(b []byte, out any) error {
	if !utf8.Valid(b) {
		return ErrUnsafe
	}
	check := json.NewDecoder(bytes.NewReader(b))
	if e := uniqueJSON(check, 0); e != nil {
		return ErrUnsafe
	}
	if _, e := check.Token(); e != io.EOF {
		return ErrUnsafe
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(out); e != nil {
		return fmt.Errorf("%w: invalid state", ErrUnsafe)
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrUnsafe
	}
	return nil
}
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrUnsafe
	}
	tok, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return e
			}
			k, ok := key.(string)
			if !ok || seen[k] {
				return ErrUnsafe
			}
			seen[k] = true
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	default:
		return ErrUnsafe
	}
	end, e := d.Token()
	if e != nil {
		return e
	}
	if (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
		return ErrUnsafe
	}
	return nil
}
