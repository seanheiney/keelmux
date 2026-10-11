package library

import (
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type bundle struct {
	pack     Package
	files    map[string][]byte
	manifest []byte
}

func verifyBundle(root *os.File, priv bool, pin *Package) (bundle, error) {
	var b bundle
	files := map[string][]byte{}
	total := 0
	entries := 0
	var walk func(*os.File, string, int) error
	walk = func(dir *os.File, prefix string, depth int) error {
		if depth > 16 {
			return ErrInvalid
		}
		// Every directory is freshly opened; ReadDir therefore starts at zero.
		names, err := dir.ReadDir(maxFiles*2 + 2)
		if err != nil && err != io.EOF {
			return err
		}
		if len(names) > maxFiles*2+1 {
			return ErrInvalid
		}
		sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })
		for _, entry := range names {
			entries++
			if entries > maxFiles*2+1 {
				return ErrInvalid
			}
			name := entry.Name()
			rel := name
			if prefix != "" {
				rel = prefix + "/" + name
			}
			if !relative(rel) {
				return ErrInvalid
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return ErrUnsafe
			}
			if entry.IsDir() {
				d, err := openAt(dir, name, true, false, priv)
				if err != nil {
					return err
				}
				before := len(files)
				err = walk(d, rel, depth+1)
				same := sameAt(dir, name, d)
				d.Close()
				if err != nil {
					return err
				}
				if !same || len(files) == before {
					return ErrUnsafe
				}
				continue
			}
			f, err := openAt(dir, name, false, false, priv)
			if err != nil {
				return err
			}
			before, err := f.Stat()
			limit := fileLimit
			if rel == ManifestFilename {
				limit = manifestLimit
			}
			if err != nil || before.Size() > int64(limit) {
				f.Close()
				return ErrInvalid
			}
			raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
			after, e := f.Stat()
			safe := e == nil && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) && sameAt(dir, name, f)
			if safe {
				if priv {
					safe = private(after, false)
				} else {
					safe = ownedSource(after, false)
				}
			}
			f.Close()
			if err != nil || len(raw) > limit {
				return ErrInvalid
			}
			if !safe {
				return ErrUnsafe
			}
			if rel == ManifestFilename {
				b.manifest = raw
			} else {
				files[rel] = raw
				total += len(raw)
				if len(files) > maxFiles || total > totalLimit {
					return ErrInvalid
				}
			}
		}
		info, err := dir.Stat()
		if err != nil || priv && !private(info, true) || !priv && !ownedSource(info, true) {
			return ErrUnsafe
		}
		return nil
	}
	if err := walk(root, "", 0); err != nil {
		return b, err
	}
	var m Manifest
	if len(b.manifest) == 0 || strictJSON(b.manifest, &m, manifestLimit) != nil || validateManifest(m) != nil {
		return b, ErrInvalid
	}
	m = normalizeManifest(m)
	if len(m.Files) != len(files) {
		return b, ErrInvalid
	}
	for name, raw := range files {
		if hashBytes(raw) != m.Files[name] {
			return b, ErrCorrupt
		}
	}
	b.pack = Package{Manifest: m, Digest: packDigest(m)}
	b.files = files
	if pin != nil {
		left, _ := json.Marshal(b.pack)
		right, _ := json.Marshal(pin)
		if string(left) != string(right) {
			return b, ErrCorrupt
		}
	}
	current, err := openPath(root.Name(), false, priv)
	if err != nil {
		return b, ErrUnsafe
	}
	defer current.Close()
	a, e := root.Stat()
	c, f := current.Stat()
	if e != nil || f != nil || !os.SameFile(a, c) {
		return b, ErrUnsafe
	}
	return b, nil
}
func writeNew(parent *os.File, name string, raw []byte) error {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name))
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	return err
}
func stageBundle(s *store, b bundle) (string, error) {
	name, err := randomName(".stage-")
	if err != nil {
		return "", err
	}
	if err = unix.Mkdirat(int(s.root.Fd()), name, 0700); err != nil {
		return "", err
	}
	root, err := openAt(s.root, name, true, false, true)
	if err != nil {
		return "", err
	}
	defer root.Close()
	manifest, _ := json.Marshal(b.pack.Manifest)
	if err = writeNew(root, ManifestFilename, manifest); err != nil {
		return name, err
	}
	for _, rel := range sortedKeys(b.files) {
		dir := root
		owned := []*os.File{}
		for _, part := range strings.Split(path.Dir(rel), "/") {
			if part == "." {
				continue
			}
			d, e := openAt(dir, part, true, true, true)
			if e != nil {
				for _, f := range owned {
					f.Close()
				}
				return name, e
			}
			owned = append(owned, d)
			dir = d
		}
		err = writeNew(dir, path.Base(rel), b.files[rel])
		if err == nil {
			err = dir.Sync()
		}
		for i := len(owned) - 1; i >= 0; i-- {
			if e := owned[i].Sync(); err == nil {
				err = e
			}
			owned[i].Close()
		}
		if err != nil {
			return name, err
		}
	}
	if err = root.Sync(); err == nil {
		err = s.root.Sync()
	}
	return name, err
}
