package library

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestGuardedSelectionsRefuseStaleRevisionAndPinUnderWriterLock(t *testing.T) {
	r, state := fixtureRegistry(t)
	a, b := imported(t, r, "pack", "1"), imported(t, r, "pack", "2")
	p, err := r.SelectPackage(bg, "default", "pack", a.Digest, true)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := r.SelectPackage(bg, "default", "pack", b.Digest, true)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, state)
	if _, err = r.SelectComponentAt(bg, "default", "pack", "skill/review", false, p.Revision, a.Digest); !errors.Is(err, ErrStale) {
		t.Fatal("stale component changed new pin", err)
	}
	if _, err = r.SelectComponentAt(bg, "default", "pack", "skill/review", false, newer.Revision, a.Digest); !errors.Is(err, ErrStale) {
		t.Fatal("wrong expected pin", err)
	}
	if _, err = r.SelectPackageAt(bg, "default", "pack", a.Digest, false, p.Revision); !errors.Is(err, ErrStale) {
		t.Fatal("stale pack changed current selection", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, state)) {
		t.Fatal("refused edits changed owned bytes")
	}
	if _, err = r.SelectComponentAt(bg, "default", "pack", "skill/review", false, newer.Revision, b.Digest); err != nil {
		t.Fatal("current guarded edit refused", err)
	}
}

func TestOneConcurrentGuardedProfileEditWins(t *testing.T) {
	r, _ := fixtureRegistry(t)
	p := imported(t, r, "pack", "1")
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := r.SelectPackageAt(bg, "default", "pack", p.Digest, true, 1)
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for e := range results {
		if e == nil {
			winners++
		} else if !errors.Is(e, ErrStale) {
			t.Fatal(e)
		}
	}
	if winners != 1 {
		t.Fatal("guard failed to serialize stale edits", winners)
	}
}
