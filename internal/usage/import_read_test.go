package usage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestAtomicBatchExistingConflictLeavesFileAndProjectionUnchanged(t *testing.T) {
	l, path := openTest(t)
	existing := attempt("existing")
	ingest(t, l, existing)
	before := project(t, l, query())
	fileBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	directoryBefore, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	newRecord := attempt("new-record")
	conflict := attempt("existing")
	conflict.Attempt.ActualModel = "different"
	results, err := l.IngestBatch(context.Background(), []Observation{newRecord, conflict})
	if !errors.Is(err, ErrConflict) || results != nil {
		t.Fatalf("mixed batch did not fail atomically: %+v %v", results, err)
	}
	if !reflect.DeepEqual(before, project(t, l, query())) {
		t.Fatal("failed batch left hidden inserted observation")
	}
	fileAfter, _ := os.ReadFile(path)
	if !bytes.Equal(fileBefore, fileAfter) {
		t.Fatal("failed batch changed original database bytes")
	}
	directoryAfter, _ := os.ReadDir(filepath.Dir(path))
	if !reflect.DeepEqual(directoryBefore, directoryAfter) {
		t.Fatal("failed batch left journal/state artifacts")
	}
	results, err = l.IngestBatch(context.Background(), []Observation{newRecord, existing})
	if err != nil || len(results) != 2 || results[0].Duplicate || !results[1].Duplicate {
		t.Fatalf("atomic new/replay batch failed: %+v %v", results, err)
	}
	results, err = l.IngestBatch(context.Background(), []Observation{newRecord, existing})
	if err != nil || !results[0].Duplicate || !results[1].Duplicate {
		t.Fatal("batch replay was not durable/idempotent")
	}
}

func TestBatchPrevalidationRejectsIntrinsicConflicts(t *testing.T) {
	a := attempt("a")
	b := attempt("a")
	b.Attempt.ActualModel = "other"
	if err := ValidateBatch([]Observation{a, b}); !errors.Is(err, ErrConflict) {
		t.Fatalf("intrinsic ID conflict not caught before opening ledger: %v", err)
	}
	b = a
	b.ID = "different-observation-id"
	if err := ValidateBatch([]Observation{a, b}); !errors.Is(err, ErrConflict) {
		t.Fatalf("reassigned stream sequence not caught before mutation: %v", err)
	}
	if err := ValidateBatch(nil); err == nil {
		t.Fatal("empty batch accepted")
	}
	if err := ValidateBatch(make([]Observation, MaxBatchObservations+1)); !errors.Is(err, ErrCapacity) {
		t.Fatal("unbounded batch accepted")
	}
	b = attempt("invalid-utf8")
	b.Attempt.Context.Account = string([]byte{'a', 0xff})
	if err := ValidateBatch([]Observation{a, b}); err == nil {
		t.Fatal("invalid UTF-8 identity normalized instead of rejected")
	}
}

func TestReadonlyExistingAndMissingLedgerNeverMutateState(t *testing.T) {
	l, path := openTest(t)
	ingest(t, l, attempt("observed"))
	before, _ := os.ReadFile(path)
	filesBefore, _ := os.ReadDir(filepath.Dir(path))
	p, present, err := ReadProjection(context.Background(), path, query())
	if err != nil || !present || !reflect.DeepEqual(p, project(t, l, query())) {
		t.Fatalf("read-only projection failed: %v %v", present, err)
	}
	after, _ := os.ReadFile(path)
	filesAfter, _ := os.ReadDir(filepath.Dir(path))
	if !bytes.Equal(before, after) || !reflect.DeepEqual(filesBefore, filesAfter) {
		t.Fatal("read-only view changed data or created artifacts")
	}
	absent := filepath.Join(t.TempDir(), "absent", "usage.db")
	p, present, err = ReadProjection(context.Background(), absent, query())
	if err != nil || present || p.Coverage.Scope != Unknown || p.Counts.ToolCalls != nil || len(p.Attempts) != 0 {
		t.Fatalf("missing ledger was not empty/unknown: %+v %v", p, err)
	}
	if _, err = os.Lstat(filepath.Dir(absent)); !os.IsNotExist(err) {
		t.Fatal("missing ledger read created state")
	}
	q := query()
	q.Timezone = "not/a/timezone"
	if _, _, err = ReadProjection(context.Background(), absent, q); err == nil {
		t.Fatal("missing ledger bypassed query validation")
	}
	q = query()
	q.Authorization.AccountDetails = false
	q.Filter.Account = "private"
	if _, _, err = ReadProjection(context.Background(), absent, q); err == nil {
		t.Fatal("missing ledger bypassed projection grants")
	}
}

func TestPrivateStateRefusedWithoutPermissionRepair(t *testing.T) {
	l, path := openTest(t)
	ingest(t, l, attempt("private"))
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("public ledger silently tightened")
	}
	if _, _, err := ReadProjection(context.Background(), path, query()); err == nil {
		t.Fatal("public ledger read accepted")
	}
	info, _ := os.Stat(path)
	after, _ := os.ReadFile(path)
	if info.Mode().Perm() != 0644 || !bytes.Equal(before, after) {
		t.Fatal("unsafe ledger changed")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".lock", 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("public writer lock silently tightened")
	}
	info, _ = os.Stat(path + ".lock")
	if info.Mode().Perm() != 0644 {
		t.Fatal("unsafe lock permissions changed")
	}
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadProjection(context.Background(), path, query()); err == nil {
		t.Fatal("public directory read accepted")
	}
	info, _ = os.Stat(filepath.Dir(path))
	if info.Mode().Perm() != 0755 {
		t.Fatal("read silently tightened directory")
	}
}

func TestReadonlyRefusesSymlinksAndSchemaWithoutArtifacts(t *testing.T) {
	l, path := openTest(t)
	ingest(t, l, attempt("real"))
	l.Close()
	linkDir := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(path), linkDir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadProjection(context.Background(), filepath.Join(linkDir, "usage.db"), query()); err == nil {
		t.Fatal("symlink directory read accepted")
	}
	parent := t.TempDir()
	link := filepath.Join(parent, "alias.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadProjection(context.Background(), link, query()); err == nil {
		t.Fatal("symlink file read accepted")
	}
	broken := filepath.Join(parent, "broken.db")
	if err := os.WriteFile(broken, []byte("not sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(broken)
	if _, _, err := ReadProjection(context.Background(), broken, query()); err == nil {
		t.Fatal("invalid schema accepted")
	}
	after, _ := os.ReadFile(broken)
	if !bytes.Equal(before, after) {
		t.Fatal("readonly invalid schema was recreated")
	}
	if _, err := os.Lstat(broken + ".lock"); !os.IsNotExist(err) {
		t.Fatal("readonly schema refusal created writer lock")
	}
}

func TestCurrentNilToolCallsAndResetDoNotRestoreOldEvidence(t *testing.T) {
	l, _ := openTest(t)
	a := attempt("tools")
	a.Attempt.ToolCalls = num(5)
	ingest(t, l, a)
	b := attempt("new-tools")
	b.Attempt.Key = a.Attempt.Key
	b.Source.Sequence = 2
	b.Attempt.ToolCalls = nil
	ingest(t, l, b)
	capacity := quota("quota-reset", "host-a")
	reset := testNow.Add(10 * time.Minute)
	capacity.Quota.ResetAt = &reset
	ingest(t, l, capacity)
	capacity.ID = "reset-unavailable"
	capacity.Source.Sequence = 2
	q := *capacity.Quota
	q.ResetAt = nil
	capacity.Quota = &q
	ingest(t, l, capacity)
	p := project(t, l, query())
	if p.Counts.ToolCalls != nil || p.Counts.UnknownToolCalls != 1 {
		t.Fatalf("old tool count restored: %+v", p.Counts)
	}
	if value(p.Quotas[0].Fields, "reset_at") != "" || p.Quotas[0].Fields["reset_at"].Value != nil {
		t.Fatal("old reset time restored")
	}
}
