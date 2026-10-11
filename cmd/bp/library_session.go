package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/library/adoption"
	"github.com/seanheiney/keelmux/internal/protocol"
)

var errLibrarySessionChanged = errors.New("selected session changed during capability inspection; inspect again")

const librarySessionInventoryBytes = 8 << 20
const librarySessionInventoryCount = 4096

// Session inspection deliberately bypasses main's Ensure/start path. An entry
// can expose a launch declaration, but never supplies local filesystem authority.
func librarySessionStatus(ctx context.Context, state, entry string, args []string, diagnostics, out io.Writer) error {
	fs := flag.NewFlagSet("keelmux plugins session-status", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 || !domain.ValidObjectID(domain.ID(fs.Args()[0]), "session") {
		return errors.New("session-status requires one exact SESSION ID")
	}
	p, err := config.Resolve(state)
	if err != nil {
		return err
	}
	socket := p.Control
	localHost := domain.ID("")
	if entry != "" {
		if !filepath.IsAbs(entry) {
			return errors.New("entry socket must be an explicit absolute path")
		}
		socket = entry
	} else {
		// An absent/unverified identity remains unknown. ReadHostID never repairs
		// identity, creates state or syncs an existing directory.
		localHost, _ = config.ReadHostID(p.Dir)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c := protocol.NewClient(socket)
	defer c.HTTP.CloseIdleConnections()
	r, err := inspectLibrarySession(ctx, c, adoption.Request{StateDir: p.Dir, LocalHost: localHost}, domain.ID(fs.Args()[0]))
	if err != nil {
		return err
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(r)
}

func inspectLibrarySession(ctx context.Context, c *protocol.Client, req adoption.Request, id domain.ID) (adoption.Report, error) {
	first, err := exactLibrarySession(ctx, c, id)
	if err != nil {
		return adoption.Report{}, err
	}
	req.Session = first
	r, err := adoption.Inspect(ctx, req)
	if err != nil {
		return adoption.Report{}, err
	}
	last, err := exactLibrarySession(ctx, c, id)
	if err != nil {
		return adoption.Report{}, err
	}
	if first.Mode != last.Mode || first.Linked != last.Linked || first.Capabilities.ObserverOnly != last.Capabilities.ObserverOnly || !adoption.MatchesSession(r, last) {
		return adoption.Report{}, errLibrarySessionChanged
	}
	return r, nil
}

func exactLibrarySession(ctx context.Context, c *protocol.Client, id domain.ID) (domain.Session, error) {
	sessions, err := librarySessionInventory(ctx, c)
	if err != nil {
		return domain.Session{}, err
	}
	var selected domain.Session
	count := 0
	for _, s := range sessions {
		if s.ID == id {
			selected = s
			count++
		}
	}
	if count != 1 {
		return domain.Session{}, errors.New("session-status requires exactly one current matching session")
	}
	return selected, nil
}

// This command reads one bounded inventory per fence. It never retries, starts
// a daemon or returns arbitrary server error bodies as capability evidence.
func librarySessionInventory(ctx context.Context, c *protocol.Client) ([]domain.Session, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/sessions", nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("X-Backplane-Protocol", protocol.Version)
	res, err := c.HTTP.Do(r)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("session-status inventory unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, librarySessionInventoryBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > librarySessionInventoryBytes {
		return nil, errors.New("session-status inventory exceeds byte limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var inventory protocol.Inventory
	if err := d.Decode(&inventory); err != nil {
		return nil, errors.New("session-status inventory is invalid")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("session-status inventory contains trailing data")
	}
	if inventory.Version != protocol.Version {
		return nil, errors.New("session-status inventory protocol differs")
	}
	if len(inventory.Sessions) > librarySessionInventoryCount {
		return nil, errors.New("session-status inventory exceeds session limit")
	}
	return inventory.Sessions, nil
}
