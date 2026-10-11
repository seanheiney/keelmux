package main

import (
	"errors"
	"os"
	"sync"

	"github.com/seanheiney/keelmux/internal/federation"
	peerregistry "github.com/seanheiney/keelmux/internal/peers"
	"github.com/seanheiney/keelmux/internal/transport"
)

func entrySources(conf configuration) ([]federation.Peer, func() error, error) {
	return buildEntrySources(conf, func(path string) (*peerregistry.Registry, error) {
		return peerregistry.Open(path, nil)
	})
}

func buildEntrySources(conf configuration, open func(string) (*peerregistry.Registry, error)) (sources []federation.Peer, cleanup func() error, resultErr error) {
	registries := map[string]*peerregistry.Registry{}
	var observers []*peerregistry.ReadSource
	readStores := map[string]bool{}
	for _, peer := range conf.Peers {
		if peer.TerminalRead != nil {
			readStores[peer.InventoryStore] = true
		}
	}
	var closeOnce sync.Once
	var closeErr error
	closeAll := func() error {
		closeOnce.Do(func() {
			for _, source := range observers {
				closeErr = errors.Join(closeErr, source.Close())
			}
			for _, r := range registries {
				closeErr = errors.Join(closeErr, r.Close())
			}
		})
		return closeErr
	}
	complete := false
	defer func() {
		if !complete {
			resultErr = errors.Join(resultErr, closeAll())
		}
	}()
	for _, peer := range conf.Peers {
		if peer.Socket != "" {
			sources = append(sources, federation.Peer{HostID: peer.HostID, Source: transport.NewClient(peer.Socket)})
			continue
		}
		r := registries[peer.InventoryStore]
		if r == nil {
			info, err := os.Lstat(peer.InventoryStore)
			if err != nil || !info.IsDir() {
				return nil, closeAll, errors.New("enrolled inventory_store must already exist as a private directory")
			}
			if readStores[peer.InventoryStore] {
				r, err = peerregistry.OpenReadRegistry(peer.InventoryStore)
			} else {
				r, err = open(peer.InventoryStore)
			}
			if err != nil {
				return nil, closeAll, err
			}
			registries[peer.InventoryStore] = r
		}
		if peer.TerminalRead != nil {
			source, err := peerregistry.NewReadSource(r, peer.HostID, conf.Principal, *peer.TerminalRead)
			if err != nil {
				return nil, closeAll, err
			}
			observers = append(observers, source)
			sources = append(sources, federation.Peer{HostID: peer.HostID, Source: source})
		} else {
			if _, err := r.View(peer.HostID, conf.Principal); err != nil {
				return nil, closeAll, errors.New("entry host has no enrollment for the selected principal")
			}
			sources = append(sources, federation.Peer{HostID: peer.HostID, Source: peerregistry.Source{Registry: r, HostID: peer.HostID, Principal: conf.Principal}})
		}
	}
	complete = true
	return sources, closeAll, nil
}
