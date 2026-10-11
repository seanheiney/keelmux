package captain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
)

func ValidRef(r Ref, kind string) bool {
	return domain.ValidObjectID(r.ID, kind) && r.Revision > 0 && r.Revision <= math.MaxInt64
}

func ValidDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func ValidateTarget(t Target) error {
	if !domain.ValidObjectID(t.HostID, "host") || !domain.ValidObjectID(t.SessionID, "session") || !domain.ValidObjectID(t.ThreadID, "thread") || !domain.ValidObjectID(t.ExecutionID, "execution") || t.Generation == 0 || t.Generation > math.MaxInt64 {
		return ErrConflict
	}
	return nil
}

func ValidatePrincipal(p Principal) error {
	if p.Version != Version || !ValidRef(p.Ref, PrincipalIDKind) || len(p.Roles) == 0 || len(p.Roles) > 3 {
		return ErrConflict
	}
	for i, r := range p.Roles {
		if r != Owner && r != Reader && r != Runner || i > 0 && p.Roles[i-1] >= r {
			return ErrConflict
		}
	}
	return nil
}

func HasRole(p Principal, r Role) bool {
	if !p.Active {
		return false
	}
	for _, have := range p.Roles {
		if r == have {
			return true
		}
	}
	return false
}

func ValidateGrant(g ReadGrant) error {
	if g.Version != Version || !ValidRef(g.Ref, GrantIDKind) || !domain.ValidObjectID(g.OwnerPrincipal, PrincipalIDKind) || !domain.ValidObjectID(g.RecipientPrincipal, PrincipalIDKind) || ValidateTarget(g.Source.Target) != nil || g.Source.FirstSequence < 1 {
		return ErrConflict
	}
	return nil
}

func validRefs(refs []Ref) bool {
	if len(refs) == 0 || len(refs) > MaxSources {
		return false
	}
	for i, r := range refs {
		if !ValidRef(r, GrantIDKind) || i > 0 && refs[i-1].ID >= r.ID {
			return false
		}
	}
	return true
}

func ValidateAssignment(a Assignment) error {
	if a.Version != Version || !ValidRef(a.Ref, AssignmentIDKind) || !domain.ValidObjectID(a.OwnerPrincipal, PrincipalIDKind) || !domain.ValidObjectID(a.RunnerPrincipal, PrincipalIDKind) || ValidateTarget(a.Captain) != nil || !validRefs(a.Grants) || len(a.Readers) > MaxReaders || a.PolicyVersion != PolicyVersion || a.Control != Active && a.Control != Paused && a.Control != Revoked {
		return ErrConflict
	}
	for i, r := range a.Readers {
		if !domain.ValidObjectID(r.PrincipalID, PrincipalIDKind) || !validRefs(r.Grants) || i > 0 && a.Readers[i-1].PrincipalID >= r.PrincipalID {
			return ErrConflict
		}
	}
	return nil
}

// Hash binds domain-separated canonical bytes. It is not a signature or proof
// against another process running as the same OS user.
func Hash(tag string, raw []byte) string {
	h := sha256.New()
	h.Write([]byte(tag))
	h.Write([]byte{0})
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

func Digest(tag string, v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return Hash(tag, raw)
}

func ScanDigest(s Scan) string { s.Digest = ""; return Digest("captain.scan.v1", s) }
func ReservationDigest(r Reservation) string {
	r.Ref.Digest = ""
	return Digest("captain.reservation.v1", r)
}
func ReportDigest(r Report) string { r.Digest = ""; return Digest("captain.report.v1", r) }

func Genesis(g ReadGrant) string       { return Digest("captain.source.genesis.v1", g) }
func EntryGenesis(a Assignment) string { return Digest("captain.entries.genesis.v1", a) }

func EmptyCoverage() Coverage {
	return Coverage{Kind: "observed_lifecycle_subset", NativeOutput: "none", HistoricalOrigin: "unknown"}
}

func ValidCoverage(c Coverage) bool {
	return c.Kind == "observed_lifecycle_subset" && !c.CompleteHistory && c.NativeOutput == "none" && c.HistoricalOrigin == "unknown"
}

// Classify is an exact source-derived policy. Arbitrary prefixes or event detail
// never establish completion, approval, native acceptance, or event identity.
func Classify(kind string) (Disposition, string) {
	switch kind {
	case "launch.completed", "launch.failed", "launch.outcome_uncertain", "stop.completed", "stop.failed", "stop.outcome_uncertain":
		return Meaningful, "operation_outcome"
	case "execution.running", "execution.missing", "execution.failed", "execution.stopped", "execution.recovery_required", "execution.runtime_unavailable", "execution.primary_uncertain":
		return Meaningful, "execution_state"
	case "codex.primary.verified", "codex.primary.stopped", "codex.primary.stop_fenced", "codex.primary.stdio.currentness_lost":
		return Meaningful, "primary_lifecycle"
	case "codex.callback.repair_required", "codex.rpc.outcome_uncertain":
		return Meaningful, "repair_required"
	case "launch.recorded", "launch.attempted", "stop.recorded", "stop.attempted", "attach.granted", "attach.released", "link.on", "link.off", "codex.primary.start_attempted", "codex.primary.stdio.bound", "codex.primary.stdio.imported", "codex.acquire.granted", "codex.handover.granted", "codex.connection.bound", "codex.rpc.attempted", "codex.writer.fenced":
		return Quiet, "progress"
	default:
		return Unsupported, "unsupported"
	}
}

func Findings(s Scan) []Finding {
	out := []Finding{}
	for _, e := range s.Events {
		if e.Disposition != Meaningful {
			continue
		}
		at := -1
		for i, f := range out {
			if f.Category == e.Category && f.SessionID == e.SessionID && f.OperationID == e.OperationID {
				at = i
				break
			}
		}
		if at < 0 {
			out = append(out, Finding{Category: e.Category, SessionID: e.SessionID, OperationID: e.OperationID, Sources: []EventKey{}})
			at = len(out) - 1
		}
		out[at].Sources = append(out[at].Sources, e.Key)
	}
	return out
}

func ValidateScan(s Scan) error {
	if s.Version != Version || !ValidRef(s.Assignment, AssignmentIDKind) || len(s.Before) == 0 || len(s.Before) > MaxSources || len(s.Before) != len(s.Through) || len(s.Events) > MaxScanRows || s.SourceBytes > MaxScanBytes || !ValidCoverage(s.Coverage) || !ValidDigest(s.Digest) || ScanDigest(s) != s.Digest {
		return ErrConflict
	}
	for i, p := range s.Before {
		q := s.Through[i]
		if !ValidRef(p.Grant, GrantIDKind) || p.Through < 0 || q.Grant != p.Grant || q.Through < p.Through || !ValidDigest(p.AnchorDigest) || !ValidDigest(q.AnchorDigest) || i > 0 && s.Before[i-1].Grant.ID >= p.Grant.ID {
			return ErrConflict
		}
	}
	var unknown uint32
	seen := map[EventKey]bool{}
	for _, e := range s.Events {
		d, category := Classify(e.Kind)
		if !domain.ValidObjectID(e.Key.HostID, "host") || !domain.ValidObjectID(e.Key.ThreadID, "thread") || e.Key.Sequence < 1 || !domain.ValidObjectID(e.SessionID, "session") || e.OperationID != "" && !ValidOperationID(e.OperationID) || !ValidDigest(e.RowDigest) || e.At.IsZero() || e.At.Location() != time.UTC || seen[e.Key] || e.Disposition != d || e.Category != category {
			return ErrConflict
		}
		if d == Unsupported {
			if e.Kind != "unsupported" {
				return ErrConflict
			}
			unknown++
		}
		seen[e.Key] = true
	}
	if unknown != s.Coverage.UnsupportedEvents {
		return ErrConflict
	}
	return nil
}

func ValidID(id domain.ID, kind string) bool { return domain.ValidObjectID(id, kind) }

// Arbitrary legacy labels stay in the source row digest, never in captions.
func ValidOperationID(id domain.ID) bool { return ValidID(id, "op") || ValidID(id, "operation") }

func ValidateReservation(r Reservation) error {
	if r.Version != Version || !ValidID(r.Ref.ID, ReservationIDKind) || !ValidDigest(r.Ref.Digest) || ReservationDigest(r) != r.Ref.Digest || !ValidRef(r.Assignment, AssignmentIDKind) || r.Ordinal == 0 || !ValidID(r.RunnerPrincipal, PrincipalIDKind) || r.RoleRevision == 0 || r.RoleRevision > math.MaxInt64 || ValidateScan(r.Scan) != nil || r.Scan.Assignment != r.Assignment || len(Findings(r.Scan)) == 0 || r.CreatedAt.IsZero() {
		return ErrCorrupt
	}
	return nil
}

func ValidateReport(r Report) error {
	if r.Version != Version || !ValidID(r.ID, ReportIDKind) || !ValidRef(r.Assignment, AssignmentIDKind) || !ValidID(r.Reservation.ID, ReservationIDKind) || !ValidDigest(r.Reservation.Digest) || ValidateScan(r.Scan) != nil || r.Scan.Assignment != r.Assignment || r.CommittedAt.IsZero() || r.Usage.ReservationsConsumed == 0 || r.Usage.NativeInvocation != "not_invoked" || r.Usage.ActualProviderTurns != nil || r.Usage.ActualTokens != nil || r.Usage.ActualCharges != "unknown" || !ValidDigest(r.Digest) || ReportDigest(r) != r.Digest || Digest("findings", r.Findings) != Digest("findings", Findings(r.Scan)) {
		return ErrCorrupt
	}
	return nil
}

func CheckRevision(current, expected uint64) error {
	if current != expected || current >= math.MaxInt64 {
		return fmt.Errorf("%w: stale revision", ErrConflict)
	}
	return nil
}

func SafeKind(s string) bool { return len(s) <= 128 && !strings.ContainsAny(s, "\x00\r\n") }
