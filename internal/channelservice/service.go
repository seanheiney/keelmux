// Package channelservice serializes local-home routing with durable claims.
// It never parses remote text as commands and has no native terminal/model API.
package channelservice

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/store"
)

type Factory func(channels.Provider) (channels.Adapter, error)
type Service struct {
	db        *store.Store
	factory   Factory
	mu        sync.Mutex
	gates     map[domain.ID]chan struct{}
	adapters  map[domain.ID]channels.Adapter
	lastFetch map[domain.ID]time.Time
	lastSend  map[domain.ID]time.Time
	wake      chan domain.ID
}

func New(ctx context.Context, db *store.Store, factory Factory) (*Service, error) {
	if db == nil || factory == nil {
		return nil, errors.New("channel service requires local authority and adapters")
	}
	if _, err := db.RecoverChannelDeliveries(ctx); err != nil {
		return nil, err
	}
	return &Service{db: db, factory: factory, gates: map[domain.ID]chan struct{}{}, adapters: map[domain.ID]channels.Adapter{}, lastFetch: map[domain.ID]time.Time{}, lastSend: map[domain.ID]time.Time{}, wake: make(chan domain.ID, 32)}, nil
}

func (s *Service) gate(ctx context.Context, id domain.ID) (func(), error) {
	s.mu.Lock()
	g := s.gates[id]
	if g == nil {
		if len(s.gates) >= 256 {
			s.mu.Unlock()
			return nil, domain.ConflictError{Reason: "channel admission capacity reached"}
		}
		g = make(chan struct{}, 1)
		g <- struct{}{}
		s.gates[id] = g
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-g:
		return func() { g <- struct{}{} }, nil
	}
}
func (s *Service) adapter(b channels.Binding) (channels.Adapter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.adapters[b.ID]; a != nil {
		return a, nil
	}
	a, err := s.factory(b.Provider)
	if err == nil {
		s.adapters[b.ID] = a
	}
	return a, err
}
func (s *Service) Wake(id domain.ID) {
	select {
	case s.wake <- id:
	default:
	}
}

// Run is quiet: navigation and intake never call a model. One bounded sync per
// binding avoids unbounded polling goroutines; the next tick drains queued work.
func (s *Service) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan domain.ID, 32)
	done := make(chan domain.ID, 32)
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-jobs:
					call, c := context.WithTimeout(ctx, 12*time.Second)
					_, _ = s.Sync(call, id)
					c()
					select {
					case done <- id:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	defer func() { cancel(); workers.Wait() }()
	scheduled := map[domain.ID]bool{}
	enqueue := func(id domain.ID) {
		if !scheduled[id] {
			select {
			case jobs <- id:
				scheduled[id] = true
			default:
			}
		}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-done:
			delete(scheduled, id)
		case id := <-s.wake:
			enqueue(id)
		case <-ticker.C:
			bindings, err := s.db.ChannelBindings(ctx)
			if err != nil {
				continue
			}
			for _, b := range bindings {
				if !b.Active {
					continue
				}
				if ctx.Err() != nil {
					return
				}
				enqueue(b.ID)
			}
		}
	}
}
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for id, a := range s.adapters {
		if err := a.Close(); err != nil && first == nil {
			first = err
		}
		delete(s.adapters, id)
	}
	return first
}

func (s *Service) Bind(ctx context.Context, r protocol.ChannelBindRequest) (protocol.ChannelStatus, error) {
	var zero protocol.ChannelStatus
	if !channels.ValidID(string(r.ID)) || len(r.Allowlist) == 0 || len(r.Allowlist) > 256 {
		return zero, channels.ErrInvalid
	}
	registry, err := s.gate(ctx, "channel-binding-registry")
	if err != nil {
		return zero, err
	}
	defer registry()
	release, err := s.gate(ctx, r.ID)
	if err != nil {
		return zero, err
	}
	defer release()
	r.Allowlist = append([]string{}, r.Allowlist...)
	sort.Strings(r.Allowlist)
	if existing, _, err := s.db.ChannelBinding(ctx, r.ID); err == nil {
		if existing.SessionID != r.SessionID || existing.Provider != r.Provider || existing.Endpoint != r.Endpoint || existing.ChannelID != r.ChannelID || existing.RootID != r.RootID || existing.CredentialRef != r.CredentialRef || !reflect.DeepEqual(existing.Allowlist, r.Allowlist) {
			return zero, domain.ConflictError{Reason: "binding ID already names a different immutable route"}
		}
		return protocol.ProjectChannel(existing), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return zero, err
	}
	bindings, err := s.db.ChannelBindings(ctx)
	if err != nil {
		return zero, err
	}
	active := 0
	for _, binding := range bindings {
		if binding.Active {
			active++
		}
	}
	if active >= 32 || len(bindings) >= 128 {
		return zero, domain.ConflictError{Reason: "local channel binding limit reached"}
	}
	session, err := s.db.Session(ctx, r.SessionID)
	if err != nil {
		return zero, err
	}
	if _, err = config.ReadPrivateSecret(r.CredentialRef); err != nil {
		return zero, err
	}
	b := channels.Binding{ID: r.ID, SessionID: session.ID, ThreadID: session.ThreadID, WorkID: session.WorkID, HostID: session.HostID, Provider: r.Provider, Endpoint: r.Endpoint, ChannelID: r.ChannelID, RootID: r.RootID, CredentialRef: r.CredentialRef, Allowlist: r.Allowlist, ActivationCutoff: time.Now().UTC(), Active: true}
	a, err := s.adapter(b)
	if err != nil {
		return zero, err
	}
	proof, err := a.Probe(ctx, b)
	if err != nil {
		return zero, errors.New("channel destination or bot identity could not be verified")
	}
	b.RealmID, b.SelfID = proof.RealmID, proof.SelfID
	if !proof.Matches(b) || time.Since(proof.ObservedAt) > time.Minute || proof.ObservedAt.After(time.Now().Add(time.Second)) {
		return zero, channels.ErrInvalid
	}
	b.ActivationCutoff = time.Now().UTC()
	b, err = channels.NormalizeBinding(b)
	if err != nil {
		return zero, err
	}
	b, err = s.db.BindChannel(ctx, b, proof)
	if err != nil {
		return zero, err
	}
	s.Wake(b.ID)
	return protocol.ProjectChannel(b), nil
}

// Disable waits for the same bounded gate as Send. A prior admitted external
// effect is settled or marked uncertain before this local disable returns.
func (s *Service) Disable(ctx context.Context, id domain.ID) error {
	if _, _, err := s.db.ChannelBinding(ctx, id); err != nil {
		return err
	}
	release, err := s.gate(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	if err = s.db.DisableChannel(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	a := s.adapters[id]
	delete(s.adapters, id)
	s.mu.Unlock()
	if a != nil {
		_ = a.Close()
	}
	return nil
}
func (s *Service) Send(ctx context.Context, r protocol.ChannelSendRequest) (protocol.ChannelSendResponse, error) {
	var zero protocol.ChannelSendResponse
	if !channels.ValidID(string(r.ID)) || !channels.ValidText(r.Text) {
		return zero, channels.ErrInvalid
	}
	m, d, err := s.db.QueueChannelMessage(ctx, channels.Message{ID: r.ID, BindingID: r.BindingID, AuthorID: "local-operator", Text: r.Text})
	if err != nil {
		return zero, err
	}
	s.Wake(r.BindingID)
	return protocol.ChannelSendResponse{Message: m, Delivery: d}, nil
}

func unknown(b channels.Binding) channels.SendResult {
	return channels.SendResult{State: channels.Uncertain, Code: "transport_uncertain", Evidence: channels.Evidence{Provider: b.Provider, Endpoint: b.Endpoint, RealmID: b.RealmID, SelfID: b.SelfID, ChannelID: b.ChannelID, RootID: b.RootID, Source: "home-delivery-authority", ObservedAt: time.Now().UTC()}}
}
func (s *Service) finish(id domain.ID, r channels.SendResult) (channels.Delivery, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.db.FinishChannelDelivery(ctx, id, r)
}

func (s *Service) Sync(ctx context.Context, id domain.ID) (protocol.ChannelSyncResponse, error) {
	out := protocol.ChannelSyncResponse{BindingID: id}
	if _, _, err := s.db.ChannelBinding(ctx, id); err != nil {
		return out, err
	}
	release, err := s.gate(ctx, id)
	if err != nil {
		return out, err
	}
	defer release()
	b, cursor, err := s.db.ChannelBinding(ctx, id)
	if err != nil {
		return out, err
	}
	if !b.Active {
		return out, domain.ConflictError{Reason: "channel binding is disabled"}
	}
	a, err := s.adapter(b)
	if err != nil {
		return out, err
	}
	s.mu.Lock()
	lastSend, lastFetch := s.lastSend[id], s.lastFetch[id]
	s.mu.Unlock()
	if time.Since(lastSend) >= time.Second {
		pending, err := s.db.PendingChannelDeliveries(ctx, id, 1)
		if err != nil {
			return out, err
		}
		if len(pending) > 0 {
			bound, message, delivery, claimed, err := s.db.ClaimChannelDelivery(ctx, pending[0].ID)
			if err != nil {
				return out, err
			}
			if claimed {
				s.mu.Lock()
				s.lastSend[id] = time.Now()
				s.mu.Unlock()
				result, sendErr := a.Send(ctx, bound, delivery, message)
				preflightFailure := result.State == channels.Failed && result.Code == "remote_rejected" && result.Evidence == (channels.Evidence{})
				if sendErr != nil || (!preflightFailure && !result.Evidence.Matches(bound)) || (result.State == channels.Delivered && !channels.ValidID(result.RemoteID)) || !channels.ValidCode(result.Code) || (result.State != channels.Delivered && result.State != channels.Failed && result.State != channels.Uncertain) {
					result = unknown(bound)
				}
				d, err := s.finish(delivery.ID, result)
				if err != nil {
					return out, err
				}
				out.Delivery = &d
			}
		}
	}
	interval := 5 * time.Second
	if b.Provider == channels.Slack {
		interval = time.Minute
	}
	if b.Provider == channels.IRC {
		interval = time.Second
	}
	if time.Since(lastFetch) < interval {
		return out, nil
	}
	s.mu.Lock()
	s.lastFetch[id] = time.Now()
	s.mu.Unlock()
	result, err := a.Fetch(ctx, b, cursor, 15)
	if err != nil {
		return out, errors.New("channel intake unavailable; delivery records remain intact")
	}
	if !result.Evidence.Matches(b) || len(result.Messages) > channels.MaxPageSize || !channels.ValidCursor(result.NextCursor) {
		return out, channels.ErrInvalid
	}
	out.Imported, err = s.db.ImportChannelMessages(ctx, b.ID, cursor, result)
	out.HasMore = result.HasMore
	return out, err
}

func (s *Service) Reconcile(ctx context.Context, id domain.ID) (channels.Delivery, error) {
	d, err := s.db.ChannelDelivery(ctx, id)
	if err != nil {
		return d, err
	}
	release, err := s.gate(ctx, d.BindingID)
	if err != nil {
		return d, err
	}
	defer release()
	d, err = s.db.ChannelDelivery(ctx, id)
	if err != nil || d.State != channels.Uncertain {
		return d, err
	}
	b, _, err := s.db.ChannelBinding(ctx, d.BindingID)
	if err != nil {
		return d, err
	}
	m, err := s.db.ChannelMessage(ctx, d.MessageID)
	if err != nil {
		return d, err
	}
	a, err := s.adapter(b)
	if err != nil {
		return d, err
	}
	result, err := a.Reconcile(ctx, b, d, m)
	if err != nil || result.State != channels.Delivered || !result.Evidence.Matches(b) || !channels.ValidID(result.RemoteID) {
		return d, nil
	}
	return s.finish(id, result)
}
