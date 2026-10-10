package ircd

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/runtime"
)

type lifecycleRecord struct {
	Format                                       int
	ServerID                                     domain.ID
	OperationID                                  domain.ID
	Session                                      domain.Session
	Ref                                          domain.RuntimeRef
	ConfigSHA256, BinarySHA256, State, StartTime string
	UpdatedAt                                    time.Time
}

func (m *Manager) operationPath(id domain.ID) string {
	return filepath.Join(m.dir, "operations", digest([]byte(id))+".json")
}
func (m *Manager) lifecyclePath() string { return filepath.Join(m.dir, "lifecycle.json") }
func (m *Manager) validateLifecycle(c configRecord, r lifecycleRecord) error {
	s := r.Session
	if r.Format != 1 || r.ServerID != c.Config.ID || !validOperationID(r.OperationID) || !validLocalID(s.ID, "session") || !validLocalID(s.HostID, "host") || !validLocalID(s.Execution.ID, "execution") || s.Execution.Generation != 1 || s.Workspace != m.dir || s.Execution.Provider != "ergo" || s.Execution.ProviderVersion != Version || r.ConfigSHA256 != c.YAMLSHA256 || r.BinarySHA256 != c.Config.Runtime.BinarySHA256 || r.UpdatedAt.IsZero() {
		return ErrRepair
	}
	switch r.State {
	case "attempted", "running", "stop_attempted", "stopped", "exited":
	default:
		return ErrRepair
	}
	if r.Ref != (domain.RuntimeRef{}) && !validRuntimeRef(r.Ref, r) {
		return ErrRepair
	}
	if r.State != "attempted" && r.Ref == (domain.RuntimeRef{}) {
		return ErrRepair
	}
	if r.StartTime != "" && !validTimestamp(r.StartTime) {
		return ErrRepair
	}
	return nil
}
func validRuntimeRef(ref domain.RuntimeRef, r lifecycleRecord) bool {
	return ref.Backend != "" && ref.ExecutionID == r.Session.Execution.ID && ref.OperationID == r.OperationID && ref.Socket != "" && filepath.IsAbs(ref.Socket)
}
func (m *Manager) loadLifecycle(c configRecord, path string) (lifecycleRecord, error) {
	var r lifecycleRecord
	e := readJSON(path, &r)
	if e != nil {
		return r, e
	}
	if e = m.validateLifecycle(c, r); e != nil {
		return lifecycleRecord{}, e
	}
	return r, nil
}
func (m *Manager) saveLifecycle(r lifecycleRecord) error {
	r.UpdatedAt = time.Now().UTC()
	if writeJSON(m.operationPath(r.OperationID), r, true) != nil {
		return ErrRepair
	}
	_, e := os.Lstat(m.lifecyclePath())
	if e != nil && !os.IsNotExist(e) {
		return ErrRepair
	}
	return writeJSON(m.lifecyclePath(), r, e == nil)
}
func (m *Manager) observe(ctx context.Context, r lifecycleRecord) (runtime.Observation, error) {
	if m.supervisor == nil {
		return runtime.Observation{}, ErrUnavailable
	}
	var o runtime.Observation
	var e error
	if r.Ref == (domain.RuntimeRef{}) {
		o, e = m.supervisor.Lookup(ctx, r.Session.Execution.ID, r.OperationID)
	} else {
		o, e = m.supervisor.Inspect(ctx, r.Ref)
	}
	if e != nil || !validRuntimeRef(o.Ref, r) || (r.Ref != (domain.RuntimeRef{}) && o.Ref != r.Ref) {
		return runtime.Observation{}, ErrUncertain
	}
	return o, nil
}
func (m *Manager) inspectStatus(ctx context.Context, c configRecord, r lifecycleRecord) (Status, error) {
	s := Status{Config: c.Config, Instance: r.Session.Execution.ID, OperationID: r.OperationID, State: r.State, StartTime: r.StartTime, ObservedAt: time.Now().UTC()}
	if r.State == "stopped" {
		return s, nil
	}
	o, e := m.observe(ctx, r)
	if e != nil {
		s.State = "uncertain"
		return s, e
	}
	if !o.Alive {
		if r.State == "stop_attempted" {
			s.State = "stopped"
		} else {
			s.State = "exited"
		}
		return s, nil
	}
	if r.State == "stop_attempted" {
		s.State = "uncertain"
		return s, ErrUncertain
	}
	proof, e := m.probe(ctx, c.Config)
	if e != nil {
		s.State = "uncertain"
		return s, e
	}
	if r.StartTime != "" && r.StartTime != proof.StartTime {
		s.State = "uncertain"
		return s, ErrConflict
	}
	s.Alive = true
	s.State = "running"
	s.Version = proof.Version
	s.GoVersion = proof.GoVersion
	s.RuntimeCommit = proof.Commit
	s.StartTime = proof.StartTime
	return s, nil
}
func (m *Manager) Status(ctx context.Context) (Status, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Status{}, e
	}
	defer unlock()
	c, e := m.readConfig(ctx)
	if e != nil || c.Stage != "ready" {
		return Status{}, ErrRepair
	}
	r, e := m.loadLifecycle(c, m.lifecyclePath())
	if os.IsNotExist(e) {
		return Status{Config: c.Config, State: "initialized", ObservedAt: time.Now().UTC()}, nil
	}
	if e != nil {
		return Status{}, e
	}
	return m.inspectStatus(ctx, c, r)
}
func (m *Manager) Start(ctx context.Context, operationID domain.ID) (Status, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Status{}, e
	}
	defer unlock()
	return m.start(ctx, operationID)
}
func (m *Manager) start(ctx context.Context, operationID domain.ID) (Status, error) {
	if m.supervisor == nil {
		return Status{}, ErrUnavailable
	}
	if !validOperationID(operationID) {
		return Status{}, ErrConflict
	}
	c, e := m.readConfig(ctx)
	if e != nil || c.Stage != "ready" {
		return Status{}, ErrRepair
	}
	current, e := m.loadLifecycle(c, m.lifecyclePath())
	if e != nil && !os.IsNotExist(e) {
		return Status{}, e
	}
	hasCurrent := e == nil
	old, e := m.loadLifecycle(c, m.operationPath(operationID))
	if e == nil {
		if hasCurrent && current.OperationID != operationID {
			return Status{}, ErrConflict
		}
		if old.State == "stopped" || old.State == "exited" || old.State == "stop_attempted" {
			return Status{}, ErrConflict
		}
		s, e := m.inspectStatus(ctx, c, old)
		if e != nil {
			return s, e
		}
		if !s.Alive {
			return s, ErrConflict
		}
		o, e := m.observe(ctx, old)
		if e != nil {
			return s, e
		}
		old.Ref = o.Ref
		old.State = "running"
		old.StartTime = s.StartTime
		if m.saveLifecycle(old) != nil {
			return Status{}, ErrRepair
		}
		return s, nil
	} else if !os.IsNotExist(e) {
		return Status{}, e
	}
	if hasCurrent && current.State != "stopped" {
		s, e := m.inspectStatus(ctx, c, current)
		if e != nil {
			return s, e
		}
		if s.Alive {
			return s, ErrConflict
		}
		if s.State != "exited" && s.State != "stopped" {
			return s, ErrUncertain
		}
		current.State = s.State
		if m.saveLifecycle(current) != nil {
			return Status{}, ErrRepair
		}
	}
	dir := filepath.Join(m.dir, "operations")
	if config.EnsurePrivateDirectory(dir) != nil {
		return Status{}, ErrRepair
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) >= 4096 {
		return Status{}, ErrRepair
	}
	session := domain.Session{ID: domain.NewID("session"), HostID: domain.NewID("host"), WorkID: domain.NewID("work"), ThreadID: domain.NewID("thread"), LaneID: domain.NewID("lane"), Name: "managed IRC", Workspace: m.dir, Mode: domain.Managed, Capabilities: domain.Capabilities{Stop: true, Explanation: "Owned managed IRC process"}, CreatedAt: time.Now().UTC(), Execution: domain.Execution{ID: domain.NewID("execution"), Generation: 1, Provider: "ergo", ProviderVersion: Version}}
	r := lifecycleRecord{Format: 1, ServerID: c.Config.ID, OperationID: operationID, Session: session, ConfigSHA256: c.YAMLSHA256, BinarySHA256: c.Config.Runtime.BinarySHA256, State: "attempted", UpdatedAt: time.Now().UTC()}
	if writeJSON(m.operationPath(operationID), r, false) != nil {
		return Status{}, ErrRepair
	}
	if writeJSON(m.lifecyclePath(), r, hasCurrent) != nil {
		return Status{}, ErrRepair
	}
	spec := domain.LaunchSpec{Session: session, OperationID: operationID, Argv: []string{c.Config.Runtime.Path, "run", "--conf", filepath.Join(m.dir, "ergo.yaml"), "--quiet"}, Env: []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + m.dir, "TMPDIR=" + m.dir, "LC_ALL=C"}}
	ref, e := m.supervisor.LaunchProcess(ctx, spec)
	if e != nil || !validRuntimeRef(ref, r) {
		return Status{Config: c.Config, Instance: session.Execution.ID, OperationID: operationID, State: "uncertain", ObservedAt: time.Now().UTC()}, ErrUncertain
	}
	r.Ref = ref
	// Persist the acknowledged exact reference before waiting for the API. Failure
	// leaves the original intent for Lookup; it never authorizes another launch.
	if m.saveLifecycle(r) != nil {
		return Status{}, ErrRepair
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, e := m.inspectStatus(ctx, c, r)
		if e == nil && s.Alive {
			r.State = "running"
			r.StartTime = s.StartTime
			if m.saveLifecycle(r) != nil {
				return Status{}, ErrRepair
			}
			return s, nil
		}
		if e == nil && !s.Alive {
			return s, ErrUnavailable
		}
		if time.Now().After(deadline) {
			return s, ErrUncertain
		}
		select {
		case <-ctx.Done():
			return s, ErrUncertain
		case <-time.After(25 * time.Millisecond):
		}
	}
}
func (m *Manager) Stop(ctx context.Context, expectedInstance domain.ID) error {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return e
	}
	defer unlock()
	return m.stop(ctx, expectedInstance)
}
func (m *Manager) stop(ctx context.Context, expectedInstance domain.ID) error {
	if m.supervisor == nil {
		return ErrUnavailable
	}
	c, e := m.readConfig(ctx)
	if e != nil || c.Stage != "ready" {
		return ErrRepair
	}
	r, e := m.loadLifecycle(c, m.lifecyclePath())
	if e != nil {
		return e
	}
	if !validLocalID(expectedInstance, "execution") || expectedInstance != r.Session.Execution.ID {
		return ErrConflict
	}
	if r.State == "stopped" {
		return nil
	}
	o, e := m.observe(ctx, r)
	if e != nil {
		return e
	}
	r.Ref = o.Ref
	if !o.Alive {
		r.State = "stopped"
		return m.saveLifecycle(r)
	}
	if r.State == "stop_attempted" {
		return ErrUncertain
	}
	// Stop authority comes from the authenticated exact-instance supervisor.
	// A sick API or altered token must not prevent stopping that owned process.
	r.State = "stop_attempted"
	if m.saveLifecycle(r) != nil {
		return ErrRepair
	}
	if m.supervisor.Stop(ctx, r.Ref) != nil {
		return ErrUncertain
	}
	o, e = m.observe(ctx, r)
	if e != nil || o.Alive {
		return ErrUncertain
	}
	r.State = "stopped"
	return m.saveLifecycle(r)
}
func (m *Manager) Restart(ctx context.Context, expectedInstance, operationID domain.ID) (Status, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Status{}, e
	}
	defer unlock()
	if e = m.stop(ctx, expectedInstance); e != nil {
		return Status{}, e
	}
	return m.start(ctx, operationID)
}

// Remote account/channel operations require a current verified owned execution,
// in addition to the private TLS/API proof; an open port is not ownership.
func (m *Manager) requireLive(ctx context.Context, c configRecord) error {
	r, e := m.loadLifecycle(c, m.lifecyclePath())
	if e != nil {
		return ErrUnavailable
	}
	s, e := m.inspectStatus(ctx, c, r)
	if e != nil || !s.Alive {
		return ErrUnavailable
	}
	return nil
}
