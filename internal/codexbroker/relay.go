package codexbroker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/seanheiney/agent-backplane/internal/codexbroker/wire"
	"github.com/seanheiney/agent-backplane/internal/domain"
)

// Relay connects verified private channels. It implements Sender; the common
// authority, rather than this transport's map mutex, admits upstream writes.
type Relay struct {
	mu       sync.Mutex
	policy   Policy
	baseline Baseline
	channels map[domain.ID]*Channel
}

// Channel has one permanently bound projection and a dedicated upstream. Its
// lifetime belongs to the home controller, independently of the native UI.
type Channel struct {
	relay     *Relay
	authority Authority
	id        domain.ID
	binding   ConnectionBinding
	upstream  *wire.Conn
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	client    *wire.Conn
	attached  bool
	requests  map[string]*relayRequest
	usedIDs   map[string]bool
	settled   map[string][32]byte
	err       error
}

type relayRequest struct {
	ready   chan struct{}
	receipt Receipt
}

func NewRelay(policy Policy, baseline Baseline) (*Relay, error) {
	if missingDependency(policy) || baseline.ProtocolVersion == "" || baseline.Workspace == "" {
		return nil, errors.New("broker relay requires policy and pinned home baseline")
	}
	b := baseline
	b.WorkspaceAliases = append([]string(nil), baseline.WorkspaceAliases...)
	b.NativeBootstrap = make(map[string]json.RawMessage, len(baseline.NativeBootstrap))
	for method, value := range baseline.NativeBootstrap {
		b.NativeBootstrap[method] = append(json.RawMessage(nil), value...)
	}
	return &Relay{policy: policy, baseline: b, channels: make(map[domain.ID]*Channel)}, nil
}

func missingDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Interface, reflect.Func, reflect.Chan, reflect.Slice:
		return v.IsNil()
	}
	return false
}

// Open takes ownership of a dedicated, already-owned upstream connection only
// after authority verifies the immutable binding. It sends no initialization.
func (r *Relay) Open(ctx context.Context, authority Authority, binding ConnectionBinding, upstream *wire.Conn) (*Channel, error) {
	if missingDependency(authority) || upstream == nil || binding.Role != NativeLocal {
		return nil, errors.New("initial native relay requires verified native-local authority and upstream")
	}
	verified, err := authority.Bind(ctx, binding)
	if err != nil {
		return nil, err
	}
	ownedCtx, cancel := context.WithCancel(ctx)
	c := &Channel{relay: r, authority: authority, id: domain.NewID("channel"), binding: verified, upstream: upstream,
		ctx: ownedCtx, cancel: cancel, done: make(chan struct{}), requests: make(map[string]*relayRequest), usedIDs: make(map[string]bool), settled: make(map[string][32]byte)}
	r.mu.Lock()
	r.channels[c.id] = c
	r.mu.Unlock()
	go c.readUpstream()
	return c, nil
}

func (r *Relay) WriteFrame(ctx context.Context, id domain.ID, frame []byte) error {
	r.mu.Lock()
	c := r.channels[id]
	r.mu.Unlock()
	if c == nil {
		return errors.New("owned upstream channel is unavailable")
	}
	return c.upstream.Write(ctx, frame)
}

// RunNative consumes a single fresh native UI. Ending this method closes that
// UI only; the home must explicitly Close its primary channel. Old bindings and
// endpoints cannot be attached again, including by the same principal.
func (c *Channel) RunNative(ctx context.Context, client *wire.Conn) error {
	if client == nil {
		return errors.New("native UI channel is required")
	}
	c.mu.Lock()
	if c.attached {
		c.mu.Unlock()
		return errors.New("native endpoint incarnation has already been consumed")
	}
	c.attached, c.client = true, client
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.client = nil
		c.mu.Unlock()
		_ = client.Close()
	}()
	for {
		raw, err := client.Read(ctx)
		if err != nil {
			return err
		}
		frame, err := c.relay.policy.Decode(raw)
		if err != nil {
			return errors.New("native UI sent an invalid broker frame")
		}
		class, err := c.relay.policy.Classify(c.binding, frame, c.relay.baseline)
		if err != nil || class == Denied || class == Mutation || frame.Kind == Result || frame.Kind == RPCError {
			if err := c.refuse(frame.ID); err != nil {
				return err
			}
			continue
		}
		if class == ViewerLocal {
			// A local unsubscribe cannot touch the authoritative upstream anchor.
			if err := c.localResult(frame.ID); err != nil {
				return err
			}
			continue
		}
		if class != NativeBootstrap && class != ScopedRead {
			return errors.New("broker policy returned an unsupported initial effect")
		}
		intent := Intent{OperationID: domain.NewID("operation"), Origin: c.binding, UpstreamChannelID: c.id,
			DownstreamRequestID: append(json.RawMessage(nil), frame.ID...), Frame: frame}
		var pending *relayRequest
		if frame.Kind == Request {
			c.mu.Lock()
			if c.usedIDs[string(frame.ID)] || len(c.usedIDs) >= 4096 || len(c.requests) >= 256 {
				c.mu.Unlock()
				return errors.New("native request ID reused or endpoint request bound exceeded")
			}
			c.usedIDs[string(frame.ID)] = true
			upstreamID, _ := json.Marshal(string(domain.NewID("rpc")))
			intent.Frame.ID = upstreamID
			pending = &relayRequest{ready: make(chan struct{})}
			c.requests[string(upstreamID)] = pending
			c.mu.Unlock()
		}
		receipt, dispatchErr := c.authority.Dispatch(ctx, intent)
		if pending != nil {
			pending.receipt = receipt
			close(pending.ready)
		}
		if dispatchErr != nil {
			// An invoked uncertain write is retained by authority and never retried.
			if err := c.refuse(frame.ID); err != nil {
				return err
			}
		}
	}
}

func (c *Channel) readUpstream() {
	defer close(c.done)
	for {
		raw, err := c.upstream.Read(c.ctx)
		if err != nil {
			c.finish(err)
			return
		}
		frame, err := c.relay.policy.Decode(raw)
		if err != nil {
			c.finish(errors.New("owned upstream sent an invalid pinned frame"))
			return
		}
		output := OutputContext{Recipient: c.binding, UpstreamChannelID: c.id}
		if frame.Kind == Result || frame.Kind == RPCError {
			encoded, err := Encode(frame)
			if err != nil {
				c.finish(errors.New("upstream reply frame could not be retained"))
				return
			}
			hash := sha256.Sum256(encoded)
			c.mu.Lock()
			pending := c.requests[string(frame.ID)]
			oldHash, known := c.settled[string(frame.ID)]
			c.mu.Unlock()
			if pending == nil {
				if known && oldHash == hash {
					continue // Exact duplicate of a previously settled source frame.
				}
				// Unknown or changed replies may represent an unmapped effect.
				// Preserve a blocker rather than manufacturing idle quiescence.
				if err := c.quarantine(frame.ID, encoded); err != nil {
					c.finish(err)
					return
				}
				continue
			}
			select {
			case <-pending.ready:
			case <-c.ctx.Done():
				c.finish(c.ctx.Err())
				return
			}
			if pending.receipt.Intent.OperationID == "" {
				continue
			}
			settlement := pending.receipt
			settlement.State = Acknowledged
			if frame.Kind == RPCError {
				settlement.State, settlement.Detail = Failed, "owned upstream rejected the recorded request"
			}
			if _, err := c.authority.Settle(c.ctx, settlement); err != nil {
				c.finish(errors.New("upstream result could not settle its original durable intent"))
				return
			}
			original := pending.receipt.Intent.Frame
			output.OriginalRequest, output.OriginOperationID = &original, pending.receipt.Intent.OperationID
			if err := c.deliver(output, frame, pending.receipt.Intent.DownstreamRequestID); err != nil {
				// Client loss does not stop the authoritative upstream reader.
				c.detachClient()
			}
			c.mu.Lock()
			delete(c.requests, string(frame.ID))
			c.settled[string(frame.ID)] = hash
			c.mu.Unlock()
			continue
		}
		if frame.Kind == Request {
			// Initial profile has no model turns or approval routing. Preserve the
			// callback as unattributed/non-actionable before any possible display.
			callbackFrame, err := Encode(frame)
			if err != nil {
				c.finish(errors.New("upstream callback frame could not be retained"))
				return
			}
			if err := c.quarantine(frame.ID, callbackFrame); err != nil {
				c.finish(err)
				return
			}
			// No native/typed response can answer it in this initial profile.
			continue
		}
		if err := c.deliver(output, frame, nil); err != nil {
			c.detachClient()
		}
	}
}

func (c *Channel) quarantine(id json.RawMessage, frame []byte) error {
	record := CallbackRecord{ID: domain.NewID("callback"), Origin: c.binding,
		Key:     CallbackKey{UpstreamChannelID: c.id, RequestID: append(json.RawMessage(nil), id...)},
		Payload: append(json.RawMessage(nil), frame...)}
	if _, err := c.authority.RecordCallback(c.ctx, record); err != nil {
		return errors.New("unattributed upstream frame could not be durably retained")
	}
	return nil
}

func (c *Channel) deliver(output OutputContext, frame Frame, downstreamID json.RawMessage) error {
	return c.authority.Deliver(c.ctx, c.binding, func(ctx context.Context) error {
		projected, visible, err := c.relay.policy.NativeOutput(output, frame)
		if err != nil || !visible {
			return err
		}
		if frame.Kind == Result || frame.Kind == RPCError {
			projected.ID = downstreamID
		}
		encoded, err := Encode(projected)
		if err != nil {
			return err
		}
		c.mu.Lock()
		client := c.client
		c.mu.Unlock()
		if client == nil {
			return nil
		}
		return client.Write(ctx, encoded)
	})
}

func (c *Channel) refuse(id json.RawMessage) error {
	if len(id) == 0 {
		return nil
	}
	return c.writeLocal(Frame{Kind: RPCError, ID: id, Payload: json.RawMessage(`{"code":-32601,"message":"broker request denied"}`)})
}

func (c *Channel) localResult(id json.RawMessage) error {
	if len(id) == 0 {
		return nil
	}
	return c.writeLocal(Frame{Kind: Result, ID: id, Payload: json.RawMessage(`{}`)})
}

func (c *Channel) writeLocal(frame Frame) error {
	return c.authority.Deliver(c.ctx, c.binding, func(ctx context.Context) error {
		encoded, err := Encode(frame)
		if err != nil {
			return err
		}
		c.mu.Lock()
		client := c.client
		c.mu.Unlock()
		if client == nil {
			return errors.New("native UI is detached")
		}
		return client.Write(ctx, encoded)
	})
}

func (c *Channel) detachClient() {
	c.mu.Lock()
	client := c.client
	c.client = nil
	c.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

func (c *Channel) finish(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	c.detachClient()
}

func (c *Channel) Close() error {
	c.cancel()
	_ = c.upstream.Close()
	c.detachClient()
	<-c.done
	c.relay.mu.Lock()
	delete(c.relay.channels, c.id)
	c.relay.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Encode preserves omitted params and typed correlation IDs; Decode/policy and
// the common authority must have validated this host-internal frame first.
func Encode(frame Frame) ([]byte, error) {
	value := make(map[string]any)
	if len(frame.ID) != 0 {
		value["id"] = frame.ID
	}
	switch frame.Kind {
	case Request, Notification:
		value["method"] = frame.Method
		if len(frame.Payload) != 0 {
			value["params"] = frame.Payload
		}
	case Result:
		value["result"] = frame.Payload
	case RPCError:
		value["error"] = frame.Payload
	default:
		return nil, fmt.Errorf("unknown native frame kind %q", frame.Kind)
	}
	return json.Marshal(value)
}
