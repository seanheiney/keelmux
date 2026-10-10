package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
)

const MaxBatchObservations = 128

type preparedObservation struct {
	observation Observation
	data        []byte
	kind, key   string
}

func prepareObservation(o Observation) (preparedObservation, error) {
	if err := o.Validate(); err != nil {
		return preparedObservation{}, err
	}
	o = normalized(o)
	data, err := json.Marshal(o)
	if err != nil {
		return preparedObservation{}, err
	}
	if len(data) > MaxObservationBytes {
		return preparedObservation{}, ErrCapacity
	}
	kind, key := canonical(o)
	return preparedObservation{observation: o, data: data, kind: kind, key: key}, nil
}

// ValidateBatch validates every payload without creating state or changing a
// ledger. It does not replace transaction-time identity/capacity checks.
func ValidateBatch(observations []Observation) error {
	_, err := prepareBatch(observations)
	return err
}
func prepareBatch(observations []Observation) ([]preparedObservation, error) {
	if len(observations) == 0 {
		return nil, errors.New("usage import requires at least one observation")
	}
	if len(observations) > MaxBatchObservations {
		return nil, ErrCapacity
	}
	out := make([]preparedObservation, 0, len(observations))
	identities := map[string][]byte{}
	sequences := map[string]string{}
	sources := map[string]Source{}
	for _, o := range observations {
		p, err := prepareObservation(o)
		if err != nil {
			return nil, err
		}
		id := stringJSON([]string{o.Source.ID, o.ID})
		if old, ok := identities[id]; ok && !bytes.Equal(old, p.data) {
			return nil, ErrConflict
		}
		identities[id] = p.data
		sequence := stringJSON([]any{o.Source.ID, p.kind, p.key, o.Source.Sequence})
		if previous, ok := sequences[sequence]; ok && previous != id {
			return nil, ErrConflict
		}
		sequences[sequence] = id
		if previous, ok := sources[o.Source.ID]; ok && (previous.Kind != o.Source.Kind || previous.Quality != o.Source.Quality) {
			return nil, ErrConflict
		}
		sources[o.Source.ID] = o.Source
		out = append(out, p)
	}
	return out, nil
}

// IngestBatch commits all observations together, or returns no results and rolls
// back all inserts. Replay/conflict rules are exactly the single-ingest rules.
func (l *Ledger) IngestBatch(ctx context.Context, observations []Observation) ([]IngestResult, error) {
	prepared, err := prepareBatch(observations)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db == nil {
		return nil, ErrClosed
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out := make([]IngestResult, 0, len(prepared))
	for _, p := range prepared {
		result, err := ingestPrepared(ctx, tx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
