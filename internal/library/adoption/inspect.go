package adoption

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/library"
	"github.com/seanheiney/keelmux/internal/library/native"
)

var hostIdentity = regexp.MustCompile(`^host_[a-f0-9]{32}$`)

// readers keeps deterministic race controls local to a test invocation. The
// public inspector always uses the existing strict read-only implementations.
type readers struct {
	open    func(string) (library.Registry, error)
	status  func(context.Context, native.Target) (native.ApplicationStatus, error)
	preview func(context.Context, library.Registry, library.Resolution, native.Target) (native.Plan, error)
}

// Inspect observes only a proven local managed session's recorded application.
// LocalHost is a trusted caller input from config.ReadHostID, not a wire claim.
// The fences establish one coherent observation, never a future guarantee
// against same-user mutation or evidence of native loading/permissions.
func Inspect(ctx context.Context, request Request) (Report, error) {
	return inspect(ctx, request, readers{library.Open, native.Status, native.Preview})
}

func inspect(ctx context.Context, request Request, read readers) (Report, error) {
	s := request.Session
	out := reportFor(s)
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !hostIdentity.MatchString(string(request.LocalHost)) {
		return refused(out, Unavailable, "local_host_unproven", "inspect_local_session"), nil
	}
	if s.HostID != request.LocalHost {
		return refused(out, Unavailable, "remote_session", "inspect_local_session"), nil
	}
	if s.Mode != domain.Managed || s.Linked || s.Capabilities.ObserverOnly {
		return refused(out, Unavailable, "unmanaged_session", "inspect_local_session"), nil
	}
	if !domain.ValidObjectID(s.ID, "session") || !domain.ValidObjectID(s.Execution.ID, "execution") || s.Execution.Generation == 0 || !boundedReference(s.Execution.State) {
		return refused(out, Unavailable, "session_invalid", "inspect_launch"), nil
	}
	if s.Library == nil {
		return refused(out, Unavailable, "launch_missing", "inspect_launch"), nil
	}
	if s.Library.Validate() != nil {
		return refused(out, Unavailable, "launch_invalid", "inspect_launch"), nil
	}
	switch s.Execution.Provider {
	case "codex", "claude", "gemini", "grok", "opencode":
	default:
		return refused(out, Unavailable, "provider_unsupported", "inspect_launch"), nil
	}
	if !absolutePath(s.Workspace) || s.Workspace == "/" {
		return refused(out, Unavailable, "workspace_invalid", "inspect_launch"), nil
	}
	if s.Library.ApplicationID == "" {
		return refused(out, Unavailable, "application_missing", "inspect_launch"), nil
	}
	if !absolutePath(request.StateDir) || request.StateDir == "/" {
		return refused(out, Unavailable, "state_invalid", "inspect_registry"), nil
	}
	reg, err := read.open(request.StateDir)
	if err != nil {
		return readFailure(ctx, out, err, "registry_unavailable", "inspect_registry")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	resolution, err := reg.Resolve(ctx, s.Library.ProfileID)
	if err != nil {
		return readFailure(ctx, out, err, "profile_unavailable", "inspect_registry")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !validSelection(resolution, s.Library.ProfileID) {
		return refused(out, Blocked, "registry_unavailable", "inspect_registry"), nil
	}
	out.Saved = selectionFor(resolution)
	out.SavedToLaunch = comparison(savedMatchesLaunch(*out.Saved, *out.Launch))
	target := native.Target{Harness: s.Execution.Provider, Workspace: s.Workspace, ApplicationID: s.Library.ApplicationID}
	first, err := read.status(ctx, target)
	out.Applied = applicationFor(target, first)
	if err != nil {
		return nativeFailure(ctx, out, err)
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !first.Present || first.Receipt == nil {
		return refused(out, Pending, "application_absent", "preview_apply"), nil
	}
	if !first.Verified || first.Pending || first.Receipt.Target != target {
		return refused(out, Blocked, "application_unsafe", "inspect_application"), nil
	}
	if !first.Receipt.Applied {
		return refused(out, Pending, "application_not_applied", "preview_apply"), nil
	}
	if first.Receipt.Coverage != "complete_for_declared_resolution" || len(first.Receipt.Unmapped) > 0 {
		return refused(out, Blocked, "application_partial", "preview_apply"), nil
	}
	plan, err := read.preview(ctx, reg, resolution, target)
	if err != nil {
		return nativeFailure(ctx, out, err)
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if len(plan.Collisions) > 0 {
		return refused(out, Blocked, "mapping_collision", "inspect_application"), nil
	}
	if len(plan.Unmapped) > 0 {
		return refused(out, Blocked, "mapping_unmapped", "preview_apply"), nil
	}
	// The selected revision/resolution and receipt must survive the mapping
	// observation. Unrelated catalog revisions do not invalidate this profile.
	current, err := reg.Resolve(ctx, s.Library.ProfileID)
	if err != nil {
		return readFailure(ctx, out, err, "observation_changed", "refresh")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !sameResolution(resolution, current) {
		return refused(out, Blocked, "observation_changed", "refresh"), nil
	}
	last, err := read.status(ctx, target)
	if err != nil {
		return nativeFailure(ctx, out, err)
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !last.Present || !last.Verified || last.Pending || last.Receipt == nil || !sameReceipt(first.Receipt, last.Receipt) {
		return refused(out, Blocked, "observation_changed", "refresh"), nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	out.SavedToApplied = comparison(savedMatchesReceipt(*out.Saved, *last.Receipt) && receiptMatchesPlan(*last.Receipt, plan))
	out.AppliedToLaunch = comparison(receiptMatchesLaunch(*last.Receipt, *out.Launch))
	if out.SavedToApplied != Match {
		return refused(out, Pending, "application_changed", "preview_apply"), nil
	}
	if out.SavedToLaunch != Match || out.AppliedToLaunch != Match {
		return refused(out, Pending, "saved_changed", "new_session_required"), nil
	}
	return refused(out, Aligned, "aligned", "none"), nil
}

func reportFor(s domain.Session) Report {
	out := Report{HostID: s.HostID, SessionID: s.ID, ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Provider: s.Execution.Provider, Workspace: s.Workspace, ExecutionState: s.Execution.State, SavedToLaunch: Unknown, SavedToApplied: Unknown, AppliedToLaunch: Unknown, State: Unavailable, Reason: "launch_missing", NextAction: "inspect_launch", NativeLoaded: Unknown, Permissions: Unknown}
	if s.Library != nil {
		copy := *s.Library
		out.Launch = &copy
	}
	return out
}

// MatchesSession excludes a result after any captured execution or launch
// declaration changes. Callers additionally fence mode/linked/observer eligibility
// and their screen/profile epoch, including unchanged unavailable reports.
func MatchesSession(report Report, session domain.Session) bool {
	if report.HostID != session.HostID || report.SessionID != session.ID || report.ExecutionID != session.Execution.ID || report.Generation != session.Execution.Generation || report.Provider != session.Execution.Provider || report.Workspace != session.Workspace || report.ExecutionState != session.Execution.State {
		return false
	}
	if report.Launch == nil || session.Library == nil {
		return report.Launch == nil && session.Library == nil
	}
	return *report.Launch == *session.Library
}

func refused(out Report, state, reason, action string) Report {
	out.State, out.Reason, out.NextAction = state, reason, action
	return out
}
func readFailure(ctx context.Context, out Report, err error, reason, action string) (Report, error) {
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return out, err
	}
	return refused(out, Blocked, reason, action), nil
}
func nativeFailure(ctx context.Context, out Report, err error) (Report, error) {
	reason, action := "application_unsafe", "inspect_application"
	switch {
	case errors.Is(err, native.ErrPending):
		reason, action = "application_pending", "recover_application"
	case errors.Is(err, native.ErrDrift):
		reason = "application_drift"
	case errors.Is(err, native.ErrStale):
		reason, action = "observation_changed", "refresh"
	case errors.Is(err, native.ErrCollision):
		reason = "mapping_collision"
	case errors.Is(err, native.ErrUnsupported):
		reason, action = "mapping_unmapped", "preview_apply"
	}
	return readFailure(ctx, out, err, reason, action)
}
func comparison(same bool) string {
	if same {
		return Match
	}
	return Different
}
func selectionFor(r library.Resolution) *Selection {
	return &Selection{r.ProfileID, r.ProfileRevision, r.Digest}
}
func applicationFor(target native.Target, status native.ApplicationStatus) *Application {
	a := &Application{ID: target.ApplicationID, Present: status.Present, Verified: status.Verified}
	if status.Receipt != nil {
		a.Digest, a.Coverage = status.Receipt.Digest, status.Receipt.Coverage
	}
	return a
}
func savedMatchesLaunch(saved Selection, launch domain.LibrarySnapshot) bool {
	return saved.ProfileID == launch.ProfileID && saved.ProfileRevision == launch.ProfileRevision && saved.ResolutionDigest == launch.ResolutionDigest
}
func savedMatchesReceipt(saved Selection, receipt native.Receipt) bool {
	return saved.ProfileID == receipt.ProfileID && saved.ProfileRevision == receipt.ProfileRevision && saved.ResolutionDigest == receipt.ResolutionDigest
}
func receiptMatchesLaunch(receipt native.Receipt, launch domain.LibrarySnapshot) bool {
	return receipt.Target.ApplicationID == launch.ApplicationID && receipt.Digest == launch.ApplicationDigest && receipt.ProfileID == launch.ProfileID && receipt.ProfileRevision == launch.ProfileRevision && receipt.ResolutionDigest == launch.ResolutionDigest
}
func sameResolution(a, b library.Resolution) bool {
	a.CatalogRevision, b.CatalogRevision = 0, 0
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func sameReceipt(a, b *native.Receipt) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func receiptMatchesPlan(receipt native.Receipt, plan native.Plan) bool {
	if plan.ExpectedReceiptDigest != receipt.Digest || plan.Target != receipt.Target || plan.ProfileID != receipt.ProfileID || plan.ProfileRevision != receipt.ProfileRevision || plan.ResolutionDigest != receipt.ResolutionDigest || len(receipt.LaunchArgs) != len(plan.LaunchArgs) {
		return false
	}
	for i, arg := range receipt.LaunchArgs {
		if arg != plan.LaunchArgs[i] {
			return false
		}
	}
	files := make(map[string]native.OwnedFile, len(receipt.Files))
	for _, f := range receipt.Files {
		if _, duplicate := files[f.Path]; duplicate {
			return false
		}
		files[f.Path] = f
	}
	if len(plan.Files) != len(files) {
		return false
	}
	for _, op := range plan.Files {
		f, ok := files[op.Path]
		if !op.After.Present || !ok || f.SHA256 != op.After.SHA256 || f.Bytes != len(op.Content) {
			return false
		}
		delete(files, op.Path)
	}
	return len(files) == 0
}
func boundedReference(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}
func absolutePath(value string) bool {
	return value != "" && len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl) && filepath.IsAbs(value) && filepath.Clean(value) == value
}
func validSelection(r library.Resolution, profile string) bool {
	if r.ProfileID != profile || r.ProfileRevision == 0 || len(r.Digest) != 64 || r.Loaded != Unknown {
		return false
	}
	for _, c := range r.Digest {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
