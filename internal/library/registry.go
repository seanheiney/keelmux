package library

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type registry struct {
	stateDir string
	mu       sync.Mutex
	fault    func(string) error
}

var _ Registry = (*registry)(nil)

// Open observes the explicitly selected local state without creating it. A
// pending operation remains recoverable through the next explicit mutation.
func Open(stateDir string) (Registry, error) {
	if !absolute(stateDir) {
		return nil, ErrUnsafe
	}
	r := &registry{stateDir: stateDir}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := r.openStore(ctx, false)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	defer s.close()
	rs, err := loadRecords(s)
	if err != nil {
		return nil, err
	}
	pending, err := existingIntent(s)
	if err != nil {
		return nil, err
	}
	if pending == nil {
		if err = verifyRecords(s, rs); err != nil {
			return nil, err
		}
	}
	if !s.anchored() {
		return nil, ErrUnsafe
	}
	return r, nil
}
func randomName(prefix string) (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:]), err
}
func (r *registry) checkpoint(point string) error {
	if r.fault != nil {
		return r.fault(point)
	}
	return nil
}
func (r *registry) lock(ctx context.Context) error {
	for !r.mu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return err
	}
	return nil
}
func (r *registry) read(ctx context.Context, fn func(*store, records) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.lock(ctx); err != nil {
		return err
	}
	defer r.mu.Unlock()
	s, err := r.openStore(ctx, false)
	if os.IsNotExist(err) {
		return fn(nil, emptyRecords())
	}
	if err != nil {
		return err
	}
	defer s.close()
	in, err := existingIntent(s)
	if err != nil {
		return err
	}
	if in != nil {
		return ErrPending
	}
	rs, err := loadRecords(s)
	if err != nil {
		return err
	}
	if err = verifyRecords(s, rs); err != nil {
		return err
	}
	if err = fn(s, rs); err != nil {
		return err
	}
	if !s.anchored() {
		return ErrUnsafe
	}
	return ctx.Err()
}
func (r *registry) mutate(ctx context.Context, fn func(*store, *records) (bool, *move, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.lock(ctx); err != nil {
		return err
	}
	defer r.mu.Unlock()
	s, err := r.openStore(ctx, true)
	if err != nil {
		return err
	}
	defer s.close()
	rs, err := loadRecords(s)
	if err != nil {
		return err
	}
	pending, err := existingIntent(s)
	if err != nil {
		return err
	}
	for _, name := range []string{"packs", "archive"} {
		d, e := openAt(s.root, name, true, rs.Revision == 0 && pending == nil, true)
		if e != nil {
			return e
		}
		d.Close()
	}
	rs, err = r.recover(s, rs)
	if err != nil {
		return err
	}
	if err = verifyRecords(s, rs); err != nil {
		return err
	}
	before := recordsHash(rs)
	catalogIdentity, err := fileIdentity(s.root, "catalog.json")
	if err != nil {
		return err
	}
	changed, m, err := fn(s, &rs)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if rs.Revision == ^uint64(0) {
		return ErrInvalid
	}
	rs.Revision++
	if err = validateRecords(rs); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if !s.anchored() {
		return ErrUnsafe
	}
	current, err := fileIdentity(s.root, "catalog.json")
	if err != nil {
		return err
	}
	if current != catalogIdentity {
		return ErrCorrupt
	}
	in := intent{Schema: SchemaVersion, Before: before, After: rs, Move: m}
	if err = atomicFile(s.root, "pending.json", in, ""); err != nil {
		return err
	}
	pendingIdentity, err := fileIdentity(s.root, "pending.json")
	if err != nil {
		return err
	}
	if err = r.checkpoint("intent"); err != nil {
		return err
	}
	if m != nil {
		if err = performMove(s, m, func() error {
			if err := unchanged(s, "catalog.json", catalogIdentity); err != nil {
				return err
			}
			return unchanged(s, "pending.json", pendingIdentity)
		}); err != nil {
			return err
		}
	}
	if err = r.checkpoint("move"); err != nil {
		return err
	}
	if err = verifyRecords(s, rs); err != nil {
		return err
	}
	if !s.anchored() {
		return ErrUnsafe
	}
	if err = atomicFile(s.root, "catalog.json", rs, catalogIdentity); err != nil {
		return err
	}
	if err = r.checkpoint("catalog"); err != nil {
		return err
	}
	if err = unchanged(s, "pending.json", pendingIdentity); err != nil {
		return err
	}
	if err = unix.Unlinkat(int(s.root.Fd()), "pending.json", 0); err != nil {
		return err
	}
	return s.root.Sync()
}
func (r *registry) Read(ctx context.Context) (Catalog, error) {
	out := Catalog{Schema: SchemaVersion, Packages: []Package{}, Profiles: []Profile{}}
	err := r.read(ctx, func(s *store, rs records) error {
		out.Present = rs.Revision > 0
		out.Revision = rs.Revision
		for _, k := range sortedKeys(rs.Packages) {
			out.Packages = append(out.Packages, rs.Packages[k])
		}
		for _, k := range sortedKeys(rs.Profiles) {
			out.Profiles = append(out.Profiles, rs.Profiles[k])
		}
		return nil
	})
	return out, err
}
func (r *registry) Import(ctx context.Context, source string) (Package, error) {
	if err := ctx.Err(); err != nil {
		return Package{}, err
	}
	d, err := openPath(source, false, false)
	if err != nil {
		return Package{}, err
	}
	b, err := verifyBundle(d, false, nil)
	d.Close()
	if err != nil {
		return Package{}, err
	}
	err = r.mutate(ctx, func(s *store, rs *records) (bool, *move, error) {
		if _, ok := rs.Packages[b.pack.Digest]; ok {
			return false, nil, nil
		}
		if _, ok := rs.Archived[b.pack.Digest]; ok {
			return false, nil, errors.New("exact capability revision is archived; preserve it and import a distinct revision")
		}
		if len(rs.Packages)+len(rs.Archived) >= 256 {
			return false, nil, ErrInvalid
		}
		staged, err := stageBundle(s, b)
		if err != nil {
			return false, nil, err
		}
		rs.Packages[b.pack.Digest] = b.pack
		return true, &move{From: staged, To: "packs/" + b.pack.Digest, Package: b.pack}, nil
	})
	return b.pack, err
}
func (r *registry) CreateProfile(ctx context.Context, id, name string) (Profile, error) {
	if !validID(id) || name == "" || !validText(name, 128) {
		return Profile{}, ErrInvalid
	}
	var out Profile
	err := r.mutate(ctx, func(s *store, rs *records) (bool, *move, error) {
		if p, ok := rs.Profiles[id]; ok {
			out = p
			if p.Name != name {
				return false, nil, ErrInvalid
			}
			return false, nil, nil
		}
		if len(rs.Profiles) >= 64 {
			return false, nil, ErrInvalid
		}
		out = Profile{ID: id, Name: name, Revision: 1, Packages: map[string]Selection{}}
		rs.Profiles[id] = out
		return true, nil, nil
	})
	return out, err
}
func (r *registry) SelectPackage(ctx context.Context, profileID, packageID, digest string, enabled bool) (Profile, error) {
	if !validID(profileID) || !validID(packageID) || !digestPattern.MatchString(digest) {
		return Profile{}, ErrInvalid
	}
	var out Profile
	err := r.mutate(ctx, func(s *store, rs *records) (bool, *move, error) {
		p, ok := rs.Profiles[profileID]
		if !ok {
			return false, nil, ErrNotFound
		}
		pack, ok := rs.Packages[digest]
		if !ok || pack.Manifest.ID != packageID {
			return false, nil, ErrNotFound
		}
		prior, selected := p.Packages[packageID]
		if selected && prior.Digest == digest && prior.Enabled == enabled {
			out = p
			return false, nil, nil
		}
		if p.Revision == ^uint64(0) {
			return false, nil, ErrInvalid
		}
		components := map[string]bool{}
		if selected && prior.Digest == digest {
			components = prior.Components
		}
		p.Packages[packageID] = Selection{Digest: digest, Enabled: enabled, Components: components}
		p.Revision++
		rs.Profiles[profileID] = p
		out = p
		return true, nil, nil
	})
	return out, err
}
func (r *registry) SelectComponent(ctx context.Context, profileID, packageID, key string, enabled bool) (Profile, error) {
	if !validID(profileID) || !validID(packageID) {
		return Profile{}, ErrInvalid
	}
	var out Profile
	err := r.mutate(ctx, func(s *store, rs *records) (bool, *move, error) {
		p, ok := rs.Profiles[profileID]
		if !ok {
			return false, nil, ErrNotFound
		}
		sel, ok := p.Packages[packageID]
		if !ok {
			return false, nil, ErrNotFound
		}
		pack, active := rs.Packages[sel.Digest]
		if !active {
			pack = rs.Archived[sel.Digest]
		}
		if !componentExists(pack.Manifest, key) {
			return false, nil, ErrInvalid
		}
		prior, explicit := sel.Components[key]
		if !explicit {
			prior = true
		}
		if prior == enabled {
			out = p
			return false, nil, nil
		}
		if p.Revision == ^uint64(0) {
			return false, nil, ErrInvalid
		}
		if sel.Components == nil {
			sel.Components = map[string]bool{}
		}
		if enabled {
			delete(sel.Components, key)
		} else {
			sel.Components[key] = false
		}
		p.Packages[packageID] = sel
		p.Revision++
		rs.Profiles[profileID] = p
		out = p
		return true, nil, nil
	})
	return out, err
}
func (r *registry) Resolve(ctx context.Context, profileID string) (Resolution, error) {
	if !validID(profileID) {
		return Resolution{}, ErrInvalid
	}
	out := Resolution{ProfileID: profileID, Loaded: "unknown", Packages: []Package{}, Skills: []ResolvedAsset{}, Rules: []ResolvedAsset{}, Tools: []ResolvedTool{}}
	err := r.read(ctx, func(s *store, rs records) error {
		p, ok := rs.Profiles[profileID]
		if !ok {
			return ErrNotFound
		}
		out.ProfileRevision = p.Revision
		out.CatalogRevision = rs.Revision
		for _, id := range sortedKeys(p.Packages) {
			sel := p.Packages[id]
			if !sel.Enabled {
				continue
			}
			pack := rs.Packages[sel.Digest]
			out.Packages = append(out.Packages, pack)
			on := func(key string) bool { v, ok := sel.Components[key]; return !ok || v }
			for _, a := range pack.Manifest.Skills {
				if on("skill/" + a.ID) {
					out.Skills = append(out.Skills, ResolvedAsset{PackageID: id, PackageDigest: pack.Digest, Asset: a})
				}
			}
			for _, a := range pack.Manifest.Rules {
				if on("rule/" + a.ID) {
					out.Rules = append(out.Rules, ResolvedAsset{PackageID: id, PackageDigest: pack.Digest, Asset: a})
				}
			}
			for _, t := range pack.Manifest.Tools {
				if on("tool/" + t.ID) {
					out.Tools = append(out.Tools, ResolvedTool{PackageID: id, PackageDigest: pack.Digest, Tool: t})
				}
			}
		}
		// Revisions are observations, not applied-content identity. Unrelated
		// imports or profile edits must not mark this selection reload pending.
		raw, _ := json.Marshal(p.Packages)
		out.Digest = hashBytes(raw)
		return nil
	})
	return out, err
}
func (r *registry) PackageRoot(ctx context.Context, id, digest string) (string, error) {
	if !validID(id) || !digestPattern.MatchString(digest) {
		return "", ErrInvalid
	}
	var out string
	err := r.read(ctx, func(s *store, rs records) error {
		p, ok := rs.Packages[digest]
		if !ok || p.Manifest.ID != id {
			return ErrNotFound
		}
		out = s.root.Name() + "/packs/" + digest
		return nil
	})
	return out, err
}
func (r *registry) Remove(ctx context.Context, id, digest string) error {
	if !validID(id) || !digestPattern.MatchString(digest) {
		return ErrInvalid
	}
	return r.mutate(ctx, func(s *store, rs *records) (bool, *move, error) {
		p, ok := rs.Packages[digest]
		if !ok {
			if old, ok := rs.Archived[digest]; ok && old.Manifest.ID == id {
				return false, nil, nil
			}
			return false, nil, ErrNotFound
		}
		if p.Manifest.ID != id {
			return false, nil, ErrNotFound
		}
		for key, profile := range rs.Profiles {
			sel, ok := profile.Packages[id]
			if ok && sel.Digest == digest && sel.Enabled {
				if profile.Revision == ^uint64(0) {
					return false, nil, ErrInvalid
				}
				sel.Enabled = false
				profile.Packages[id] = sel
				profile.Revision++
				rs.Profiles[key] = profile
			}
		}
		delete(rs.Packages, digest)
		rs.Archived[digest] = p
		return true, &move{From: "packs/" + digest, To: "archive/" + digest, Package: p}, nil
	})
}
