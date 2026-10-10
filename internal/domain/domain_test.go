package domain

import "testing"

func TestStableIdentityDoesNotFollowRuntime(t *testing.T) {
	s := NewSession("host_a", "same name", "/tmp", "codex", "unknown")
	other := NewSession("host_a", "same name", "/tmp", "codex", "unknown")
	if s.ID == other.ID || s.WorkID == other.WorkID || s.ThreadID == other.ThreadID || s.Execution.ID == other.Execution.ID {
		t.Fatal("names and paths must not identify work")
	}
	work, thread, lane := s.WorkID, s.ThreadID, s.LaneID
	s.Execution.ID = NewID("execution")
	s.Execution.Generation++
	s.Execution.Runtime.PID = 42
	if s.WorkID != work || s.ThreadID != thread || s.LaneID != lane {
		t.Fatal("execution replacement changed stable identity")
	}
}
func TestUncertainCannotBeReattempted(t *testing.T) {
	if CanTransition(Uncertain, Attempted) || CanTransition(Completed, Attempted) {
		t.Fatal("must reconcile, not replay")
	}
	if !CanTransition(Uncertain, Completed) || !CanTransition(Attempted, Uncertain) {
		t.Fatal("reconciliation not expressible")
	}
}
