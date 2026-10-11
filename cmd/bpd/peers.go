package main

import (
	"context"
	"github.com/seanheiney/keelmux/internal/domain"
)

// Registry-only inventory: no reconciliation or runtime operation on export.
type homePeerInventory func(context.Context) ([]domain.Session, error)

func (f homePeerInventory) List(ctx context.Context) ([]domain.Session, error) { return f(ctx) }
