package main

import (
	"errors"
	"os"

	"github.com/seanheiney/keelmux/internal/federation"
	peerregistry "github.com/seanheiney/keelmux/internal/peers"
	"github.com/seanheiney/keelmux/internal/transport"
)

func entrySources(conf configuration) ([]federation.Peer, func(), error) {
	return buildEntrySources(conf, func(path string) (*peerregistry.Registry, error) {
		return peerregistry.Open(path, nil)
	})
}

func buildEntrySources(conf configuration, open func(string) (*peerregistry.Registry, error)) ([]federation.Peer, func(), error) {
	var sources []federation.Peer
	registries := map[string]*peerregistry.Registry{}
	closeAll := func() {
		for _, r := range registries {
			_ = r.Close()
		}
	}
	complete := false
	defer func() {
		if !complete {
			closeAll()
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
			r, err = open(peer.InventoryStore)
			if err != nil {
				return nil, closeAll, err
			}
			registries[peer.InventoryStore] = r
		}
		if _, err := r.View(peer.HostID, conf.Principal); err != nil {
			return nil, closeAll, errors.New("entry host has no enrollment for the selected principal")
		}
		sources = append(sources, federation.Peer{HostID: peer.HostID, Source: peerregistry.Source{Registry: r, HostID: peer.HostID, Principal: conf.Principal}})
	}
	complete = true
	return sources, closeAll, nil
}
