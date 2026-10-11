package diagnostics

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/seanheiney/keelmux/internal/diagnosticfs"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"golang.org/x/sys/unix"
)

func privateSocket(dir *diagnosticfs.Dir, name string) (unix.Stat_t, error) {
	s, e := dir.Stat(name)
	if e != nil {
		return s, e
	}
	if s.Mode&unix.S_IFMT != unix.S_IFSOCK || s.Mode&07777 != 0600 || s.Uid != uint32(os.Geteuid()) {
		return s, diagnosticfs.ErrUnsafe
	}
	return s, nil
}
func entryCheck(path string) Check {
	c := Check{Name: "entry_socket", State: "unavailable", Summary: "Remote entry authority is not qualified by local daemon diagnostics; no connection was made.", Action: "Run diagnostics at the execution home to inspect its daemon and registry."}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		c.State = "invalid"
		return c
	}
	d, e := diagnosticfs.OpenDir(filepath.Dir(path))
	if e != nil {
		return failed("entry_socket", e, "Entry socket directory is absent.")
	}
	defer d.Close()
	if _, e = privateSocket(d, filepath.Base(path)); e != nil {
		return failed("entry_socket", e, "Entry socket is absent.")
	}
	c.State = "socket_present"
	return c
}

func readGET(conn net.Conn, reader *bufio.Reader, path string, out any) error {
	req, _ := http.NewRequest("GET", "http://backplane"+path, nil)
	req.Header.Set("X-Backplane-Protocol", protocol.Version)
	if err := req.Write(conn); err != nil {
		return err
	}
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Backplane-Protocol") != protocol.Version {
		return os.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return os.ErrInvalid
	}
	if json.Unmarshal(raw, out) != nil {
		return os.ErrInvalid
	}
	// These existing endpoints emit concrete Go DTOs. Require that exact
	// canonical schema so duplicate/case-aliased/missing fields cannot be
	// normalized into apparently valid identity or empty channel evidence.
	canonical, err := json.Marshal(out)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(canonical, compact.Bytes()) {
		return os.ErrInvalid
	}
	return nil
}

func observeDaemon(parent context.Context, dir *diagnosticfs.Dir, host domain.ID) (*Peer, []Check) {
	c := Check{Name: "daemon", State: "unavailable", Summary: "Existing socket could not provide a verified local peer and lifecycle response.", Action: "Inspect the execution-home daemon and exact socket; no daemon was started or signalled."}
	before, e := privateSocket(dir, "control.sock")
	if e != nil {
		return nil, []Check{failed("daemon_socket", e, "No daemon socket is present; diagnostics does not start one.")}
	}
	c.State = "socket_present"
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	conn, e := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir.Path, "control.sock"))
	if e != nil {
		return nil, []Check{c}
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, []Check{c}
	}
	pid, uid, e := peerIdentity(uc)
	if e != nil || pid <= 0 || uid != os.Geteuid() {
		return nil, []Check{c}
	}
	boot, start, e := processIdentity(pid)
	if e != nil {
		return nil, []Check{c}
	}
	reader := bufio.NewReader(io.LimitReader(conn, 256<<10))
	var status protocol.DaemonStatus
	if readGET(conn, reader, "/v1/daemon", &status) != nil || status.PID != pid || status.Protocol != protocol.Version || !domain.ValidObjectID(domain.ID(status.Instance), "daemon") {
		return nil, []Check{c}
	}
	stable := func() bool {
		s, e := privateSocket(dir, "control.sock")
		b, p, pe := processIdentity(pid)
		h, he := dir.Read("host-id", 0600, 256)
		return e == nil && pe == nil && he == nil && domain.ID(h) == host && s.Ino == before.Ino && s.Dev == before.Dev && dir.Unchanged() && b == boot && p == start
	}
	if !stable() {
		return nil, []Check{c}
	}
	var channels []protocol.ChannelStatus
	ce := readGET(conn, reader, "/v1/channels", &channels)
	if channels == nil || len(channels) > 128 {
		ce = os.ErrInvalid
	}
	var inventory protocol.Inventory
	ie := readGET(conn, reader, "/v1/sessions", &inventory)
	if inventory.Version != protocol.Version || len(inventory.Sessions) > 128 {
		ie = os.ErrInvalid
	}
	var refs []domain.RuntimeRef
	for _, s := range inventory.Sessions {
		if s.HostID != host {
			ie = os.ErrInvalid
		}
		if s.Execution.Runtime.PID != 0 {
			refs = append(refs, s.Execution.Runtime)
		}
	}
	var final protocol.DaemonStatus
	if readGET(conn, reader, "/v1/daemon", &final) != nil || final.Instance != status.Instance || final.PID != pid || final.Protocol != protocol.Version || !stable() {
		return nil, []Check{c}
	}
	at := time.Now().UTC()
	c.State = "owner_process_verified"
	c.Summary = "Same-connection kernel UID/PID, boot and process lifetime match the exact lifecycle instance; executable and daemon host binding remain unverified."
	c.Action = ""
	evidence(&c, at, at)
	result := []Check{c}
	if ie != nil {
		result = append(result, Check{Name: "live_registry", State: "unavailable", Summary: "Bounded session projection could not be verified against the saved local host identity.", Action: "Inspect the local registry and daemon identity; no session repair or native request was attempted."})
	} else {
		result = append(result, Check{Name: "live_registry", State: "observed", Summary: "Saved sessions returned by the same daemon peer match the saved local host ID; registry integrity was not checked while in use.", Counts: map[string]int{"sessions": len(inventory.Sessions)}})
		liveRuntime := runtimeCheck(refs)
		liveRuntime.Name = "live_registry_runtime"
		result = append(result, liveRuntime)
	}
	if ce != nil {
		result = append(result, Check{Name: "live_channels", State: "unavailable", Summary: "Bounded read-only channel status was unavailable.", Action: "Inspect local channel configuration; no remote channel request was made."})
	} else {
		ch := Check{Name: "live_channels", State: "observed", Summary: "Read-only daemon channel projection; saved routes are not fresh authentication or delivery proof.", Counts: map[string]int{"bindings": len(channels), "active": 0, "stale_checks": 0, "unknown_checks": 0, "history_gaps": 0}}
		for _, b := range channels {
			if b.Active {
				ch.Counts["active"]++
			}
			if b.HistoryGap {
				ch.Counts["history_gaps"]++
			}
			if b.LastCheck == nil {
				ch.Counts["unknown_checks"]++
			} else if freshness(*b.LastCheck, at) != "recent" {
				ch.Counts["stale_checks"]++
			}
		}
		if ch.Counts["stale_checks"] > 0 || ch.Counts["history_gaps"] > 0 {
			ch.State = "needs_attention"
			ch.Action = "Inspect channels status at this execution home; history gaps and stale checks do not authorize reposting."
		}
		result = append(result, ch)
	}
	return &Peer{Instance: status.Instance, PID: pid, Boot: boot, Start: start, Protocol: status.Protocol}, result
}

var errIdentity = errors.New("process lifetime evidence unavailable")
