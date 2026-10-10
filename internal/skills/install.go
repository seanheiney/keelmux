package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/seanheiney/agent-backplane/internal/domain"
	"golang.org/x/sys/unix"
)

const ledgerPath = ".backplane/skills-ledger.json"
const marker = ".bp-install"

type entry struct {
	Provider       string `json:"provider"`
	Scope          string `json:"scope"`
	Directory      string `json:"directory"`
	Source         string `json:"source"`
	Revision       string `json:"revision"`
	InstallationID string `json:"installation_id"`
	Stage          string `json:"stage"`
	State          string `json:"state"`
	RemovingStage  bool   `json:"removing_stage,omitempty"`
	Files          []File `json:"files"`
}
type ledger struct {
	Version int              `json:"version"`
	Owner   string           `json:"owner"`
	Entries map[string]entry `json:"entries"`
}

// Plan never creates directories or touches native configuration.
func Plan(root string, selectedProviders []string) (Preview, error) {
	return PlanScope(root, UserScope, selectedProviders)
}
func PlanScope(root, scope string, selectedProviders []string) (Preview, error) {
	selected, err := names(selectedProviders)
	if err != nil {
		return Preview{}, err
	}
	if scope != UserScope && scope != ProjectScope {
		return Preview{}, errors.New("skill scope must be user or project")
	}
	t, err := openTree(root)
	if err != nil {
		return Preview{}, err
	}
	defer t.close()
	l, err := readLedger(t)
	if err != nil {
		return Preview{}, err
	}
	p := Preview{Root: t.path, Scope: scope, Mappings: []Mapping{}}
	for _, name := range selected {
		p.Mappings = append(p.Mappings, inspect(t, l, mapping(name, scope)))
	}
	return p, nil
}

func mapping(provider, scope string) Mapping {
	m := Mapping{Provider: provider, Scope: scope, Source: Source, Revision: Revision, Action: "install", Installed: "no", Discoverable: "unknown", Loaded: "unknown", Reload: "Restart or reload the native CLI, then verify the skill in its selector. File presence does not prove discovery or loading."}
	if provider == "grok" {
		m.Action = "unsupported"
		m.Detail = "Grok skill grammar and invocation are outside this verified mapping."
		return m
	}
	m.Directory = directory(provider)
	if provider == "codex" {
		m.Invocation = "$backplane or /skills"
	} else {
		m.Invocation = "/backplane [status|off]; preserves the native /rc command"
	}
	files := catalogue(provider)
	for name, b := range files {
		m.Files = append(m.Files, File{Path: name, Digest: digest(b)})
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return m
}
func key(scope, provider string) string { return scope + "/" + provider }
func inspect(t *tree, l ledger, m Mapping) Mapping {
	if m.Action == "unsupported" {
		return m
	}
	if e, ok := l.Entries[key(m.Scope, m.Provider)]; ok && e.State == "removing" {
		m.Action = "pending_removal"
		m.Installed = "partial"
		m.Detail = "Recorded removal requires reconciliation; use remove before any new installation."
		return m
	}
	d, err := t.dir(m.Directory, false)
	if errors.Is(err, os.ErrNotExist) {
		m.Detail = "New owned connection skill; no native configuration changes."
		if e, ok := l.Entries[key(m.Scope, m.Provider)]; ok && e.State == "installing" {
			m.Action = "recover_install"
			m.Detail = "Resume the recorded installation only after proving its staged content."
		}
		return m
	}
	if err != nil {
		m.Action = "unsafe_path"
		m.Detail = "Skill path is not a traversable real directory; symlinks and non-directories are refused."
		return m
	}
	d.Close()
	e, owned := l.Entries[key(m.Scope, m.Provider)]
	if !owned {
		m.Action = "collision"
		m.Installed = "unowned"
		m.Detail = "Existing backplane skill directory is unowned; preserve it even if its contents match."
		return m
	}
	if err := checkBundle(t, e.Directory, e.Files, false); err != nil {
		m.Action = "preserve_edited"
		m.Installed = "partial_or_edited"
		m.Detail = "Owned bundle changed or has additional content; preserve it and inspect the ownership ledger."
		return m
	}
	if e.Revision != Revision || !catalogueMatches(e, m) {
		m.Action = "preserve_revision"
		m.Installed = "owned_other_revision"
		m.Detail = "Existing owned revision differs; no automatic upgrade or overwrite is implemented."
		return m
	}
	m.Action = "unchanged"
	m.Installed = "verified_on_disk"
	m.Detail = "Owned content digests match. Native discovery and loading remain unverified."
	return m
}
func catalogueMatches(e entry, m Mapping) bool {
	want := map[string]string{}
	for _, f := range m.Files {
		want[f.Path] = f.Digest
	}
	for _, f := range e.Files {
		if f.Path == marker {
			continue
		}
		if want[f.Path] != f.Digest {
			return false
		}
		delete(want, f.Path)
	}
	return len(want) == 0
}

// Apply revalidates the approved catalogue and current files under an exclusive
// ownership-ledger lock. Independent provider results can succeed separately.
func Apply(preview Preview) ([]Result, error) {
	if preview.Scope != UserScope && preview.Scope != ProjectScope {
		return nil, errors.New("invalid preview scope")
	}
	t, err := openTree(preview.Root)
	if err != nil {
		return nil, err
	}
	defer t.close()
	var selected []string
	for _, m := range preview.Mappings {
		expected := mapping(m.Provider, preview.Scope)
		if m.Provider != "codex" && m.Provider != "claude" && m.Provider != "grok" {
			return nil, errors.New("preview names an unknown provider")
		}
		if m.Scope != expected.Scope || m.Directory != expected.Directory || m.Source != expected.Source || m.Revision != expected.Revision || !reflect.DeepEqual(m.Files, expected.Files) {
			return nil, errors.New("preview differs from the pinned skill catalogue; create a new preview")
		}
		selected = append(selected, m.Provider)
	}
	selected, err = names(selected)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return []Result{}, nil
	}
	lock, err := lockLedger(t)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	l, err := readLedger(t)
	if err != nil {
		return nil, err
	}
	var results []Result
	for _, name := range selected {
		m := inspect(t, l, mapping(name, preview.Scope))
		switch m.Action {
		case "unchanged":
			e := l.Entries[key(m.Scope, m.Provider)]
			if e.State == "installing" {
				e.State = "installed"
				l.Entries[key(m.Scope, m.Provider)] = e
				if err := writeLedger(t, l); err != nil {
					m.Action = "outcome_uncertain"
					m.Detail = "Content published, but ownership finalization failed."
				}
			}
		case "install", "recover_install":
			m = install(t, &l, m)
		}
		results = append(results, m)
	}
	return results, nil
}

func install(t *tree, l *ledger, m Mapping) Mapping {
	k := key(m.Scope, m.Provider)
	e, ok := l.Entries[k]
	if !ok || e.State != "installing" {
		id := string(domain.NewID("install"))
		e = entry{Provider: m.Provider, Scope: m.Scope, Directory: m.Directory, Source: Source, Revision: Revision, InstallationID: id, Stage: filepath.Join(".backplane", id), State: "installing", Files: append([]File{}, m.Files...)}
		e.Files = append(e.Files, File{Path: marker, Digest: digest([]byte(id + "\n"))})
		l.Entries[k] = e
		if err := writeLedger(t, *l); err != nil {
			m.Action = "failed"
			m.Detail = "Ownership intent could not be persisted; no skill published."
			return m
		}
	}
	files := catalogue(m.Provider)
	files[marker] = []byte(e.InstallationID + "\n")
	d, err := t.dir(e.Stage, false)
	if errors.Is(err, os.ErrNotExist) {
		if err = t.createDir(e.Stage); err == nil {
			err = t.write(filepath.Join(e.Stage, marker), files[marker], false)
		}
	} else if err == nil {
		d.Close()
		b, readErr := t.read(filepath.Join(e.Stage, marker))
		if readErr != nil || digest(b) != digest(files[marker]) {
			err = errors.New("recorded stage ownership is unverified")
		}
	}
	if err != nil {
		m.Action = "pending_install"
		m.Detail = "Staged installation cannot safely resume: " + err.Error()
		return m
	}
	if err := checkBundle(t, e.Stage, e.Files, true); err != nil {
		m.Action = "preserve_edited"
		m.Detail = "Recorded stage changed; preserve it for inspection."
		return m
	}
	for _, f := range e.Files {
		path := filepath.Join(e.Stage, f.Path)
		_, err := t.read(path)
		if errors.Is(err, os.ErrNotExist) {
			if err = t.write(path, files[f.Path], false); err != nil {
				m.Action = "pending_install"
				m.Detail = "Staged content write failed; recorded intent retained."
				return m
			}
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			m.Action = "pending_install"
			m.Detail = "Staged content cannot be verified."
			return m
		}
	}
	if err := checkBundle(t, e.Stage, e.Files, false); err != nil {
		m.Action = "pending_install"
		m.Detail = "Staged content verification failed."
		return m
	}
	if err := t.publish(e.Stage, e.Directory); err != nil {
		m.Action = "pending_install"
		m.Detail = "Publication failed without replacing an existing directory; recorded stage retained."
		return m
	}
	e.State = "installed"
	l.Entries[k] = e
	m.Action = "installed"
	m.Installed = "verified_on_disk"
	m.Detail = "Connection skill published with verified owned digests; native discovery and loading remain unknown."
	if err := writeLedger(t, *l); err != nil {
		m.Action = "outcome_uncertain"
		m.Detail = "Skill published; final ledger acknowledgement failed. Reapply reconciles the exact owned bundle."
	}
	return m
}

func Remove(root string, selectedProviders []string) ([]Result, error) {
	return RemoveScope(root, UserScope, selectedProviders)
}
func RemoveScope(root, scope string, selectedProviders []string) ([]Result, error) {
	selected, err := names(selectedProviders)
	if err != nil {
		return nil, err
	}
	if scope != UserScope && scope != ProjectScope {
		return nil, errors.New("skill scope must be user or project")
	}
	if len(selected) == 0 {
		return []Result{}, nil
	}
	t, err := openTree(root)
	if err != nil {
		return nil, err
	}
	defer t.close()
	lock, err := lockLedger(t)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	l, err := readLedger(t)
	if err != nil {
		return nil, err
	}
	var results []Result
	for _, name := range selected {
		m := mapping(name, scope)
		k := key(scope, name)
		e, ok := l.Entries[k]
		if m.Action == "unsupported" {
			results = append(results, m)
			continue
		}
		if !ok {
			m.Action = "not_owned"
			m.Detail = "No owned installation to remove; existing skills/configuration preserved."
			results = append(results, m)
			continue
		}
		path := e.Directory
		if e.State == "installing" || (e.State == "removing" && e.RemovingStage) {
			path = e.Stage
			if e.State == "installing" {
				if b, err := t.read(filepath.Join(e.Directory, marker)); err == nil && digest(b) == digest([]byte(e.InstallationID+"\n")) {
					path = e.Directory
				} else {
					stage, stageErr := t.dir(e.Stage, false)
					if stageErr == nil {
						stage.Close()
					} else if !errors.Is(stageErr, os.ErrNotExist) {
						m.Action = "preserve_edited"
						m.Detail = "Recorded stage has an unsafe path; preserved."
						results = append(results, m)
						continue
					} else {
						target, targetErr := t.dir(e.Directory, false)
						if targetErr == nil {
							target.Close()
							m.Action = "preserve_edited"
							m.Detail = "Publication outcome is unverified and target content may have changed; preserved."
							results = append(results, m)
							continue
						} else if !errors.Is(targetErr, os.ErrNotExist) {
							m.Action = "preserve_edited"
							m.Detail = "Publication target has an unsafe path; preserved."
							results = append(results, m)
							continue
						}
					}
				}
			}
		}
		d, err := t.dir(path, false)
		if errors.Is(err, os.ErrNotExist) {
			delete(l.Entries, k)
			if err = writeLedger(t, l); err != nil {
				return results, err
			}
			m.Action = "removed"
			m.Detail = "Owned files already absent; ledger reconciled."
			results = append(results, m)
			continue
		}
		if err == nil {
			d.Close()
			err = checkBundle(t, path, e.Files, e.State == "removing" || e.State == "installing")
		}
		if err != nil {
			m.Action = "preserve_edited"
			m.Installed = "partial_or_edited"
			m.Detail = "Owned bundle changed, includes unowned content, or has unsafe paths; removal preserved it."
			results = append(results, m)
			continue
		}
		e.State = "removing"
		e.RemovingStage = path == e.Stage
		l.Entries[k] = e
		if err := writeLedger(t, l); err != nil {
			return results, err
		}
		m.Action = "removed"
		m.Detail = "Only the matching owned bundle was removed; native configuration and other skills preserved."
		// Remove marker last, keeping interrupted removals attributable.
		for _, f := range e.Files {
			if f.Path == marker {
				continue
			}
			b, err := t.read(filepath.Join(path, f.Path))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || digest(b) != f.Digest {
				m.Action = "pending_removal"
				break
			}
			if err := t.remove(filepath.Join(path, f.Path), false); err != nil {
				m.Action = "pending_removal"
				break
			}
		}
		if m.Action == "removed" {
			for _, dir := range []string{"agents", "scripts"} {
				err := t.remove(filepath.Join(path, dir), true)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					m.Action = "pending_removal"
					break
				}
			}
		}
		if m.Action == "removed" {
			if err := t.remove(filepath.Join(path, marker), false); err != nil {
				m.Action = "pending_removal"
			}
		}
		if m.Action == "removed" {
			if err := t.remove(path, true); err != nil {
				m.Action = "pending_removal"
			}
		}
		if m.Action == "removed" {
			delete(l.Entries, k)
			if err := writeLedger(t, l); err != nil {
				m.Action = "outcome_uncertain"
			}
		}
		if m.Action != "removed" {
			m.Detail = "Removal was interrupted; ownership intent retained for reconciliation. User-changed content is not deleted."
		}
		results = append(results, m)
	}
	return results, nil
}

func lockLedger(t *tree) (*os.File, error) {
	d, err := t.dir(".backplane", true)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	fd, err := unix.Openat(int(d.Fd()), "skills-ledger.lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "skills-ledger.lock")
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("skill ledger lock is not a regular private file")
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("skill installation ledger is open in another installer")
	}
	return f, nil
}
func readLedger(t *tree) (ledger, error) {
	l := ledger{Version: 1, Owner: "agent-backplane", Entries: map[string]entry{}}
	b, err := t.read(ledgerPath)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return ledger{}, err
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return ledger{}, errors.New("ownership ledger is invalid; preserve it for inspection")
	}
	if l.Version != 1 || l.Owner != "agent-backplane" || l.Entries == nil {
		return ledger{}, errors.New("ownership ledger version/owner is unverified")
	}
	for k, e := range l.Entries {
		if e.Provider != "codex" && e.Provider != "claude" {
			return ledger{}, errors.New("ownership ledger has an unknown provider")
		}
		if (e.Scope != UserScope && e.Scope != ProjectScope) || k != key(e.Scope, e.Provider) || e.Directory != directory(e.Provider) || e.Source != Source || e.InstallationID == "" || strings.ContainsAny(e.InstallationID, "/\\") || e.Stage != filepath.Join(".backplane", e.InstallationID) || (e.State != "installing" && e.State != "installed" && e.State != "removing") {
			return ledger{}, errors.New("ownership ledger target is unverified")
		}
		seen := map[string]bool{}
		markerSeen := false
		for _, f := range e.Files {
			if _, err := components(f.Path); err != nil {
				return ledger{}, err
			}
			if seen[f.Path] || len(f.Digest) != 64 {
				return ledger{}, errors.New("ownership ledger file is invalid")
			}
			seen[f.Path] = true
			if f.Path == marker {
				markerSeen = f.Digest == digest([]byte(e.InstallationID+"\n"))
			}
		}
		if !markerSeen {
			return ledger{}, errors.New("ownership ledger correlation marker is missing")
		}
	}
	return l, nil
}
func writeLedger(t *tree, l ledger) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return t.write(ledgerPath, append(b, '\n'), true)
}

func checkBundle(t *tree, path string, files []File, allowMissing bool) error {
	expected := map[string]bool{}
	dirs := map[string]bool{}
	for _, f := range files {
		expected[f.Path] = true
		for d := filepath.Dir(f.Path); d != "."; d = filepath.Dir(d) {
			dirs[d] = true
		}
		b, err := t.read(filepath.Join(path, f.Path))
		if errors.Is(err, os.ErrNotExist) && allowMissing && f.Path != marker {
			continue
		}
		if err != nil {
			return err
		}
		if digest(b) != f.Digest {
			return errors.New("owned skill digest changed")
		}
	}
	var walk func(string) error
	walk = func(rel string) error {
		current := path
		if rel != "" {
			current = filepath.Join(path, rel)
		}
		d, err := t.dir(current, false)
		if err != nil {
			return err
		}
		entries, err := d.ReadDir(-1)
		d.Close()
		if err != nil {
			return err
		}
		for _, item := range entries {
			name := item.Name()
			if rel != "" {
				name = filepath.Join(rel, name)
			}
			if item.IsDir() {
				if !dirs[name] {
					return errors.New("unowned directory in skill bundle")
				}
				if err := walk(name); err != nil {
					return err
				}
			} else {
				if item.Type() != 0 || !expected[name] {
					return fmt.Errorf("unowned or unsafe file in skill bundle")
				}
			}
		}
		return nil
	}
	return walk("")
}
