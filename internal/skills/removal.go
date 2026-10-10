package skills

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/seanheiney/agent-backplane/internal/domain"
)

// Removal receipts outlive active installations. An archive is retained even
// after successful verification: an editor can still write through an open
// descriptor after the bundle has moved. No automatic purge deletes its bytes.
type removal struct {
	ID           string    `json:"id"`
	Installation entry     `json:"installation"`
	Source       string    `json:"source"`
	Archive      string    `json:"archive"`
	State        string    `json:"state"`
	AllowMissing bool      `json:"allow_missing,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func otherScopeOwner(l ledger, scope, provider string) (entry, bool) {
	for _, e := range l.Entries {
		if e.Directory == directory(provider) && e.Scope != scope {
			return e, true
		}
	}
	return entry{}, false
}

func removeOne(t *tree, l *ledger, m Mapping) (Mapping, error) {
	k := key(m.Scope, m.Provider)
	e, ok := l.Entries[k]
	if !ok {
		m.Action = "not_owned"
		m.Detail = "No owned installation to remove; existing skills/configuration preserved."
		var latest removal
		for _, r := range l.Removals {
			if r.Installation.Scope == m.Scope && r.Installation.Provider == m.Provider && r.CreatedAt.After(latest.CreatedAt) {
				latest = r
			}
		}
		if latest.State == "retained" || latest.State == "preserved" {
			m.RetainedPath = filepath.Join(t.path, latest.Archive)
			if exists, err := t.exists(latest.Archive); err == nil && exists {
				m.RecoveryHint = "A previous removal archive is present; inspect this exact path before any explicit cleanup."
			} else {
				m.RecoveryHint = "A previous removal receipt names this archive; its current presence is unverified or absent. No automatic cleanup is performed."
			}
		}
		return m, nil
	}
	if _, ok := otherScopeOwner(*l, m.Scope, m.Provider); ok {
		m.Action = "scope_conflict"
		m.Detail = "Another scope owns this exact directory; no removal attempted."
		return m, nil
	}
	if e.RemovalID != "" {
		return finishRemoval(t, l, e.RemovalID, m), nil
	}
	path := e.Directory
	if e.State == "installing" || (e.State == "removing" && e.RemovingStage) {
		path = e.Stage
		if e.State == "installing" {
			if b, err := t.read(filepath.Join(e.Directory, marker)); err == nil && digest(b) == digest([]byte(e.InstallationID+"\n")) {
				path = e.Directory
			} else {
				stageExists, stageErr := t.exists(e.Stage)
				if stageErr != nil {
					return preserveSource(t, m, e.Stage, "Recorded stage has an unsafe path; preserved."), nil
				}
				if !stageExists {
					targetExists, targetErr := t.exists(e.Directory)
					if targetErr != nil || targetExists {
						return preserveSource(t, m, e.Directory, "Publication outcome is unverified; target content preserved."), nil
					}
				}
			}
		}
	}
	exists, err := t.exists(path)
	if err != nil {
		return preserveSource(t, m, path, "Owned source path is unsafe; preserved."), nil
	}
	if !exists {
		delete(l.Entries, k)
		if err = writeLedger(t, *l); err != nil {
			return m, err
		}
		m.Action = "removed"
		m.Detail = "Owned source already absent; no bytes deleted and ledger reconciled."
		return m, nil
	}
	allowMissing := e.State == "removing" || e.State == "installing"
	if err = checkBundle(t, path, e.Files, allowMissing); err != nil {
		return preserveSource(t, m, path, "Owned bundle changed, includes unowned content, or lost its marker; source preserved."), nil
	}
	r, err := prepareRemoval(t, l, e, path, allowMissing)
	if err != nil {
		return m, err
	}
	return finishRemoval(t, l, r.ID, m), nil
}

func preserveSource(t *tree, m Mapping, path, detail string) Mapping {
	m.Action = "preserve_edited"
	m.Installed = "partial_or_edited"
	m.Detail = detail
	m.RecoveryHint = "Inspect " + filepath.Join(t.path, path) + "; no automatic cleanup is performed."
	return m
}

// prepareRemoval is deliberately separate from the move. Tests can exercise
// the exact boundary where a user replaces a checked filename before rename.
func prepareRemoval(t *tree, l *ledger, e entry, source string, allowMissing bool) (removal, error) {
	d, err := t.privateDir(filepath.Join(".backplane", "removals"), true)
	if err != nil {
		return removal{}, err
	}
	d.Close()
	id := string(domain.NewID("removal"))
	r := removal{ID: id, Installation: e, Source: source, Archive: filepath.Join(".backplane", "removals", id), State: "moving", AllowMissing: allowMissing, CreatedAt: time.Now().UTC()}
	if l.Removals == nil {
		l.Removals = map[string]removal{}
	}
	l.Removals[id] = r
	e.State = "removing"
	e.RemovingStage = source == e.Stage
	e.RemovalID = id
	l.Entries[key(e.Scope, e.Provider)] = e
	return r, writeLedger(t, *l)
}

func pendingArchive(t *tree, m Mapping, r removal, detail string) Mapping {
	m.Action = "pending_removal"
	m.Installed = "unknown"
	m.Detail = detail
	if exists, err := t.exists(r.Archive); err == nil && exists {
		m.RetainedPath = filepath.Join(t.path, r.Archive)
	}
	m.RecoveryHint = "Retry removal to reconcile receipt " + r.ID + "; preserve both " + filepath.Join(t.path, r.Source) + " and " + filepath.Join(t.path, r.Archive) + "."
	return m
}

// finishRemoval never touches a fresh source once the recorded archive exists.
// The whole directory is moved atomically; no live or archived file is unlinked.
func finishRemoval(t *tree, l *ledger, id string, m Mapping) Mapping {
	r, ok := l.Removals[id]
	if !ok {
		return pendingArchive(t, m, removal{ID: id}, "Removal receipt missing; no mutation attempted.")
	}
	if r.State == "restoring" {
		return restoreRemoval(t, l, r, m)
	}
	if r.State != "moving" {
		return pendingArchive(t, m, r, "Removal receipt needs explicit inspection; no further source mutation attempted.")
	}
	archived, err := t.exists(r.Archive)
	if err != nil {
		return pendingArchive(t, m, r, "Cannot inspect recorded archive; no source mutation attempted.")
	}
	if archived {
		if d, err := t.privateDir(filepath.Dir(r.Archive), false); err != nil {
			return pendingArchive(t, m, r, "Recorded archive directory is no longer private; preserve it for explicit repair.")
		} else {
			d.Close()
		}
	}
	if !archived {
		source, err := t.exists(r.Source)
		if err != nil || !source {
			return pendingArchive(t, m, r, "Source and archive outcome are unavailable; no blind retry.")
		}
		// A retry must still prove the captured owned installation. A changed
		// source stays live instead of being treated as a replacement to remove.
		if err = checkBundle(t, r.Source, r.Installation.Files, r.AllowMissing); err != nil {
			return cancelUnmovedRemoval(t, l, r, m)
		}
		if d, err := t.privateDir(filepath.Dir(r.Archive), false); err != nil {
			return pendingArchive(t, m, r, "Private archive directory unavailable; source preserved.")
		} else {
			d.Close()
		}
		if err = t.publish(r.Source, r.Archive); err != nil {
			// rename can succeed before a directory-sync acknowledgement fails.
			archived, e := t.exists(r.Archive)
			if e != nil || !archived {
				return pendingArchive(t, m, r, "Bundle move not confirmed; intent retained and no bytes deleted.")
			}
		}
	}
	if err = checkBundle(t, r.Archive, r.Installation.Files, r.AllowMissing); err != nil {
		r.State = "restoring"
		l.Removals[r.ID] = r
		if err = writeLedger(t, *l); err != nil {
			m = pendingArchive(t, m, r, "Moved bundle differs; restoration intent acknowledgement failed; archive retained.")
			m.Action = "outcome_uncertain"
			return m
		}
		return restoreRemoval(t, l, r, m)
	}
	r.State = "retained"
	l.Removals[r.ID] = r
	clearRemovedBinding(l, r)
	m.Action = "removed"
	m.Installed = "no"
	m.RetainedPath = filepath.Join(t.path, r.Archive)
	m.RecoveryHint = "Safety archive retained indefinitely; inspect this exact path before any separately authorized cleanup."
	m.Detail = "Owned bundle moved out of its installation directory and retained intact in a private safety archive; native discovery/loading remain unverified."
	if source, e := t.exists(r.Source); e != nil || source {
		m.Installed = "unowned_replacement_or_unknown"
		m.Detail = "Recorded owned bundle archived; fresh or unavailable source namespace was preserved without another move."
	}
	if err = writeLedger(t, *l); err != nil {
		m.Action = "outcome_uncertain"
		m.Detail = "Archive retained; final removal acknowledgement failed. Retry reconciles the same receipt without moving a fresh source."
	}
	return m
}

func clearRemovedBinding(l *ledger, r removal) {
	k := key(r.Installation.Scope, r.Installation.Provider)
	if e, ok := l.Entries[k]; ok && e.InstallationID == r.Installation.InstallationID && e.RemovalID == r.ID {
		delete(l.Entries, k)
	}
}

func cancelUnmovedRemoval(t *tree, l *ledger, r removal, m Mapping) Mapping {
	// The recorded archive is absent and the source no longer matches. Cancel
	// the pending move; never move a new source just to complete an old intent.
	r.State = "cancelled"
	l.Removals[r.ID] = r
	if e, ok := l.Entries[key(r.Installation.Scope, r.Installation.Provider)]; ok && e.RemovalID == r.ID {
		l.Entries[key(e.Scope, e.Provider)] = r.Installation
	}
	m = preserveSource(t, m, r.Source, "Checked source changed before move; removal cancelled and all bytes preserved.")
	if err := writeLedger(t, *l); err != nil {
		m.Action = "outcome_uncertain"
		m.Detail = "Changed source preserved; cancellation acknowledgement failed."
	}
	return m
}

func restoreRemoval(t *tree, l *ledger, r removal, m Mapping) Mapping {
	archived, err := t.exists(r.Archive)
	if err != nil {
		return pendingArchive(t, m, r, "Restoration cannot inspect the archive; all bytes retained.")
	}
	if archived {
		if d, err := t.privateDir(filepath.Dir(r.Archive), false); err != nil {
			return pendingArchive(t, m, r, "Restoration archive directory is no longer private; preserve it for explicit repair.")
		} else {
			d.Close()
		}
		if err = t.publish(r.Archive, r.Source); err != nil {
			archiveStillExists, archiveErr := t.exists(r.Archive)
			sourceExists, sourceErr := t.exists(r.Source)
			if archiveErr != nil || sourceErr != nil {
				return pendingArchive(t, m, r, "Restoration outcome uncertain; preserve both namespaces.")
			}
			if archiveStillExists {
				if !sourceExists {
					return pendingArchive(t, m, r, "Restoration blocked; private archive retained for explicit repair.")
				}
				r.State = "preserved"
				l.Removals[r.ID] = r
				clearRemovedBinding(l, r)
				m.Action = "preserve_edited_retained"
				m.Installed = "unowned_replacement"
				m.RetainedPath = filepath.Join(t.path, r.Archive)
				m.Detail = "Moved bundle changed; original namespace is occupied. Both the fresh source and retained archive were preserved."
				m.RecoveryHint = "Inspect the retained archive and original source; restoration never overwrites an occupied path."
				if err = writeLedger(t, *l); err != nil {
					m.Action = "outcome_uncertain"
					m.Detail = "Both namespaces preserved; final restoration acknowledgement failed."
				}
				return m
			}
			// Move completed but directory-sync acknowledgement was lost.
		}
	}
	source, err := t.exists(r.Source)
	if err != nil || !source {
		return pendingArchive(t, m, r, "Restoration result unavailable; no blind source mutation.")
	}
	r.State = "restored"
	l.Removals[r.ID] = r
	if e, ok := l.Entries[key(r.Installation.Scope, r.Installation.Provider)]; ok && e.RemovalID == r.ID {
		l.Entries[key(e.Scope, e.Provider)] = r.Installation
	}
	m = preserveSource(t, m, r.Source, "Moved bundle changed during removal and was restored without deleting any bytes.")
	if err = writeLedger(t, *l); err != nil {
		m.Action = "outcome_uncertain"
		m.Detail = "Bundle restored; final restoration acknowledgement failed. Retry will not move it again."
	}
	return m
}

func validateRemoval(r removal, id string) error {
	if r.ID != id || id == "" || filepath.Base(id) != id || id == "." || id == ".." || r.Archive != filepath.Join(".backplane", "removals", id) {
		return errors.New("removal receipt archive is unverified")
	}
	if r.Source != r.Installation.Directory && r.Source != r.Installation.Stage {
		return errors.New("removal receipt source is unverified")
	}
	if r.Installation.RemovalID != "" {
		return errors.New("removal snapshot cannot name another removal")
	}
	if r.AllowMissing && r.Installation.State != "installing" && r.Installation.State != "removing" {
		return errors.New("removal receipt cannot weaken a completed installation's digest checks")
	}
	switch r.State {
	case "moving", "restoring", "restored", "cancelled", "retained", "preserved":
		return nil
	default:
		return errors.New("removal receipt state is unverified")
	}
}
