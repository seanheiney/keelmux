// Package diagnostics observes existing state. It never invokes startup,
// recovery, authentication, provider turns or external-channel writes.
package diagnostics

import (
	"context"
	"errors"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/diagnosticfs"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
	"github.com/seanheiney/keelmux/internal/store"
)

type Check struct {
	Name       string         `json:"name"`
	State      string         `json:"state"`
	Summary    string         `json:"summary"`
	Action     string         `json:"action,omitempty"`
	ObservedAt *time.Time     `json:"observed_at,omitempty"`
	Freshness  string         `json:"freshness,omitempty"`
	Counts     map[string]int `json:"counts,omitempty"`
}
type Peer struct {
	Instance string `json:"instance"`
	PID      int    `json:"pid"`
	Boot     string `json:"boot"`
	Start    string `json:"process_start"`
	Protocol string `json:"protocol"`
}
type Report struct {
	Version  int       `json:"version"`
	At       time.Time `json:"observed_at"`
	StateDir string    `json:"state_dir"`
	Checks   []Check   `json:"checks"`
	Daemon   *Peer     `json:"daemon,omitempty"`
	Coverage string    `json:"coverage"`
}

func freshness(at, now time.Time) string {
	if at.IsZero() {
		return "unknown"
	}
	if at.After(now.Add(time.Second)) {
		return "invalid_future"
	}
	if now.Sub(at) > 5*time.Minute {
		return "stale"
	}
	return "recent"
}
func evidence(c *Check, at, now time.Time) {
	c.Freshness = freshness(at, now)
	if !at.IsZero() {
		v := at.UTC()
		c.ObservedAt = &v
	}
}
func failed(name string, err error, missing string) Check {
	c := Check{Name: name, State: "invalid", Summary: "Private state is unsafe, corrupt or incompatible.", Action: "Inspect ownership and permissions; restore the original state from a trusted backup. Diagnostics made no repairs."}
	if os.IsNotExist(err) {
		c.State = "missing"
		c.Summary = missing
		c.Action = "Choose the existing execution-home state directory; initialize only through the explicit product workflow."
	}
	return c
}

func Collect(ctx context.Context, p config.Paths, entry string, probeVersions bool) Report {
	r := Report{Version: 1, At: time.Now().UTC(), StateDir: p.Dir, Checks: []Check{}, Coverage: "Read-only local evidence. Socket peer ownership and process lifetime do not prove daemon host binding or loaded executable authenticity. Saved runtime/configuration evidence does not prove native readiness. Model, auth and quota are unobserved."}
	dir, err := diagnosticfs.OpenDir(p.Dir)
	if err != nil {
		r.Checks = append(r.Checks, failed("state_directory", err, "State directory does not exist."))
	} else {
		defer dir.Close()
		r.Checks = append(r.Checks, Check{Name: "state_directory", State: "ok", Summary: "Existing owned private directory."})
		host, he := dir.Read("host-id", 0600, 256)
		if he == nil && !domain.ValidObjectID(domain.ID(host), "host") {
			he = diagnosticfs.ErrUnsafe
		}
		if he != nil {
			r.Checks = append(r.Checks, failed("host_identity", he, "Saved host identity is missing."))
		} else {
			r.Checks = append(r.Checks, Check{Name: "host_identity", State: "ok", Summary: "Saved local host identity is structurally valid; no identity was created."})
		}
		place, pe := dir.Read("place.json", 0600, 4<<20)
		if pe == nil {
			_, pe = clientstate.Decode(place)
		}
		if pe != nil {
			r.Checks = append(r.Checks, failed("client_place", pe, "No saved client place; this is normal before first use."))
		} else {
			r.Checks = append(r.Checks, Check{Name: "client_place", State: "ok", Summary: "Saved client place validates; private drafts and attachment data are omitted."})
		}
		snapshot, se := store.DiagnoseExisting(ctx, dir)
		if se == nil {
			c := Check{Name: "registry", State: "ok", Summary: "Offline registry integrity and current authority/channel/project schemas validate.", Counts: map[string]int{"sessions": snapshot.Sessions, "bindings": snapshot.Bindings, "active_bindings": snapshot.ActiveBindings}}
			evidence(&c, snapshot.LastEvent, r.At)
			if c.Freshness == "stale" || c.Freshness == "invalid_future" {
				c.Action = "Inspect existing daemon and session evidence before relying on saved runtime state; event time is not liveness proof."
			}
			r.Checks = append(r.Checks, c, runtimeCheck(snapshot.Runtimes))
			r.Checks = append(r.Checks, Check{Name: "channel_configuration", State: "ok", Summary: "Saved immutable channel bindings and receipt provenance validate; remote authentication and delivery were not probed."})
		} else if errors.Is(se, store.ErrDiagnosticBusy) {
			r.Checks = append(r.Checks, Check{Name: "registry", State: "in_use", Summary: "Registry authority is locked; offline inspection was skipped.", Action: "Use the verified live daemon status below; no authority lock was changed."})
		} else {
			r.Checks = append(r.Checks, failed("registry", se, "Registry or its existing authority lock is missing."))
		}
		irc, ie := dir.Child("irc")
		if ie == nil {
			v, e := ircd.DiagnoseExisting(ctx, irc)
			ie = e
			if ie == nil {
				c := Check{Name: "ircd_configuration", State: "ok", Summary: "Saved managed IRC configuration, recorded runtime digests and release metadata validate. Live server/authentication unobserved."}
				if v.Stage != "ready" {
					c.State = "incomplete"
					c.Action = "Inspect the interrupted IRC initialization before an explicit retry."
				}
				evidence(&c, v.UpdatedAt, r.At)
				r.Checks = append(r.Checks, c)
			}
			irc.Close()
		}
		if ie != nil {
			if errors.Is(ie, ircd.ErrNotInitialized) {
				ie = os.ErrNotExist
			}
			r.Checks = append(r.Checks, failed("ircd_configuration", ie, "Managed IRC is not initialized; use ircd init only when wanted."))
		}
		if entry == "" && he == nil {
			peer, checks := observeDaemon(ctx, dir, domain.ID(host))
			r.Daemon = peer
			r.Checks = append(r.Checks, checks...)
		} else if entry == "" {
			r.Checks = append(r.Checks, Check{Name: "daemon", State: "unavailable", Summary: "Socket was not contacted because local host identity is missing or invalid.", Action: "Inspect the saved host identity before contacting local authority."})
		}
		if !dir.Unchanged() {
			r.Daemon = nil
			r.Checks = []Check{{Name: "state_directory", State: "changed", Summary: "State directory changed during inspection; collected evidence was discarded.", Action: "Inspect the directory and rerun diagnostics."}}
		}
	}
	if entry != "" {
		r.Checks = append(r.Checks, entryCheck(entry))
	}
	r.Checks = append(r.Checks, providerChecks(ctx, probeVersions)...)
	return r
}

func runtimeCheck(refs []domain.RuntimeRef) Check {
	c := Check{Name: "saved_native_runtime", State: "unobserved", Summary: "Saved process lifetime evidence only; no helper or native transport was contacted.", Counts: map[string]int{"matching": 0, "stale": 0, "unavailable": 0}}
	for _, ref := range refs {
		if !validSavedLifetime(ref) {
			c.Counts["unavailable"]++
			continue
		}
		boot, start, err := processIdentity(ref.PID)
		if errors.Is(err, os.ErrNotExist) || err == nil && (boot != ref.BootID || start != ref.ProcessStart) {
			c.Counts["stale"]++
		} else if err != nil || ref.BootID == "" || ref.ProcessStart == "" {
			c.Counts["unavailable"]++
		} else {
			c.Counts["matching"]++
		}
	}
	if c.Counts["stale"] > 0 {
		c.State = "stale"
		c.Action = "Inspect the saved execution and use explicit reconcile only after reviewing its identity; diagnostics did not signal a PID."
	} else if c.Counts["unavailable"] > 0 {
		c.State = "unavailable"
		c.Action = "Inspect the execution's native runtime evidence at its home; missing or unsupported lifetime fields do not prove process death."
	}
	return c
}

var bootRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var darwinStartRE = regexp.MustCompile(`^darwin:[1-9][0-9]*:[0-9]+$`)
var linuxStartRE = regexp.MustCompile(`^linux-ticks:[1-9][0-9]*$`)

func validSavedLifetime(ref domain.RuntimeRef) bool {
	platform, uuid, ok := strings.Cut(ref.BootID, ":")
	if ref.Backend != "native" || ref.PID <= 0 || !ok || platform != runtime.GOOS || !bootRE.MatchString(uuid) || uuid == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	return platform == "darwin" && darwinStartRE.MatchString(ref.ProcessStart) || platform == "linux" && linuxStartRE.MatchString(ref.ProcessStart)
}
