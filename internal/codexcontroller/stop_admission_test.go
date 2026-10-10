package codexcontroller

import (
	"context"
	"errors"
	"testing"
)

func TestWrongStopIdentityIsRejectedBeforeCommonGate(t *testing.T) {
	for _, field := range []string{"incarnation", "generation", "operation"} {
		t.Run(field, func(t *testing.T) {
			f := setup(t)
			v := f.start(t)
			r := f.stop(v)
			switch field {
			case "incarnation":
				r.Incarnation = "other"
			case "generation":
				r.Target.Generation++
			case "operation":
				r.OperationID = f.r.OperationID
			}
			entered := make(chan struct{})
			f.g.entered = entered
			if _, err := f.c.Stop(context.Background(), r); !errors.Is(err, ErrConflict) || f.s.stops != 0 {
				t.Fatal("stale request reached stop effect", err)
			}
			select {
			case <-entered:
				t.Fatal("stale stop entered common gate before identity check")
			default:
			}
		})
	}
}
