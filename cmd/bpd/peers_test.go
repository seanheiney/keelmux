package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/peers"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func TestHomePeerExportReadsRegistryWithEmptyHostIdentity(t *testing.T) {
	host := domain.NewID("host")
	calls := 0
	h := peers.HomeHandler(host, homePeerInventory(func(ctx context.Context) ([]domain.Session, error) { calls++; return []domain.Session{}, nil }))
	r := httptest.NewRequest("GET", peers.InventoryPath, nil)
	r.Header.Set("X-Backplane-Protocol", protocol.Version)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var s peers.Snapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil || s.HostID != host || len(s.Sessions) != 0 || calls != 1 {
		t.Fatal("empty host authority was lost or extra read performed", w.Code, w.Body.String(), calls)
	}
}
