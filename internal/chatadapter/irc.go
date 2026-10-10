package chatadapter

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/config"
)

const ircBufferSize = 256

// IRCDialer is injectable for isolated protocol fixtures. Production always
// uses the default verified TLS dialer; bindings still require an ircs endpoint.
type IRCDialer func(context.Context, string) (net.Conn, error)

// IRC keeps one authenticated connection per binding. It offers bounded live
// intake, not server history or reliable replay after disconnection/overflow.
type IRC struct {
	mu     sync.Mutex
	dial   IRCDialer
	states map[string]*ircSession
	closed bool
}

func NewIRC() *IRC {
	return NewIRCWithDialer(func(ctx context.Context, authority string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(authority)
		if err != nil {
			return nil, errTransport
		}
		d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}}
		return d.DialContext(ctx, "tcp", authority)
	})
}

func NewIRCWithDialer(dial IRCDialer) *IRC {
	return &IRC{dial: dial, states: map[string]*ircSession{}}
}

// IRC credentials are a private JSON file containing account, password and an
// optional nick. Only the server's services account establishes identity.
type ircSecret struct {
	Account  string `json:"account"`
	Password string `json:"password"`
	Nick     string `json:"nick"`
}
type ircMessage struct {
	Tags            map[string]string
	Prefix, Command string
	Params          []string
}
type ircBuffered struct {
	Sequence uint64
	Message  channels.RemoteMessage
}
type ircCursor struct {
	Binding, Generation string
	Sequence            uint64
}
type ircPending struct {
	ID     string
	Hash   [32]byte
	Text   string
	Result chan channels.SendResult
}
type ircReceipt struct {
	Hash   [32]byte
	Text   string
	Result channels.SendResult
}
type ircBatch struct {
	Label     string
	Candidate *channels.SendResult
	Invalid   bool
}
type ircSession struct {
	mu               sync.Mutex
	writeMu          sync.Mutex
	conn             net.Conn
	reader           *bufio.Reader
	proof            channels.Evidence
	binding          channels.Binding
	secretHash       [32]byte
	nick, generation string
	alive            bool
	done             chan struct{}
	wg               sync.WaitGroup
	sequence         uint64
	buffer           []ircBuffered
	pending          map[string]*ircPending
	receipts         map[string]ircReceipt
	order            []string
	batches          map[string]*ircBatch
}

func ircEndpoint(b channels.Binding) (string, error) {
	canonical, err := channels.CanonicalEndpoint(channels.IRC, b.Endpoint)
	if err != nil || canonical != b.Endpoint {
		return "", errBinding
	}
	u, err := url.Parse(b.Endpoint)
	if err != nil || b.Provider != channels.IRC || u.Scheme != "ircs" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.String() != b.Endpoint || u.Host != strings.ToLower(u.Host) || b.RootID != "" {
		return "", errBinding
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host == "" {
		return "", errBinding
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 || strconv.Itoa(n) != port || net.JoinHostPort(host, port) != u.Host {
		return "", errBinding
	}
	if !ircChannel(b.ChannelID) || b.RealmID != "" && b.RealmID != u.Host {
		return "", errBinding
	}
	return u.Host, nil
}

func ircChannel(s string) bool {
	canonical, err := channels.CanonicalChannel(channels.IRC, s)
	return err == nil && canonical == s
}

func sameIRCChannel(wire, pinned string) bool {
	canonical, err := channels.CanonicalChannel(channels.IRC, wire)
	return err == nil && canonical == pinned
}

func ircNick(s string) bool {
	if len(s) < 1 || len(s) > 30 {
		return false
	}
	for i, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || strings.ContainsRune("[]\\`_^{|}", r) || i > 0 && (r >= '0' && r <= '9' || r == '-')) {
			return false
		}
	}
	return true
}

func loadIRC(ref string) (ircSecret, [32]byte, error) {
	var secret ircSecret
	b, err := config.ReadPrivateSecret(ref)
	if err != nil || !validJSON(b) {
		return secret, [32]byte{}, errCredential
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if d.Decode(&secret) != nil || !channels.ValidID(secret.Account) || strings.ContainsAny(secret.Account, " \t\x00") || secret.Password == "" || !utf8.ValidString(secret.Password) || strings.ContainsRune(secret.Password, 0) || secret.Nick != "" && !ircNick(secret.Nick) {
		return ircSecret{}, [32]byte{}, errCredential
	}
	return secret, sha256.Sum256(b), nil
}

func randomIRC() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

func (a *IRC) session(ctx context.Context, b channels.Binding) (*ircSession, error) {
	authority, err := ircEndpoint(b)
	if err != nil || !channels.ValidID(string(b.ID)) {
		return nil, errBinding
	}
	secret, hash, err := loadIRC(b.CredentialRef)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.dial == nil {
		return nil, errTransport
	}
	if old := a.states[string(b.ID)]; old != nil {
		old.mu.Lock()
		matching := old.binding.Endpoint == b.Endpoint && old.binding.ChannelID == b.ChannelID && old.binding.CredentialRef == b.CredentialRef && matchIdentity(b, old.proof.RealmID, old.proof.SelfID)
		alive := old.alive
		sameSecret := old.secretHash == hash
		old.mu.Unlock()
		if !matching {
			return nil, errBinding
		}
		if alive && sameSecret {
			return old, nil
		}
		old.terminate()
		old.wg.Wait()
		delete(a.states, string(b.ID))
	}
	if len(a.states) >= 64 {
		return nil, errTransport
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := a.dial(bounded, authority)
	if err != nil {
		return nil, errTransport
	}
	s := &ircSession{conn: conn, reader: bufio.NewReaderSize(conn, 8706), binding: b, secretHash: hash, done: make(chan struct{}), pending: map[string]*ircPending{}, receipts: map[string]ircReceipt{}, batches: map[string]*ircBatch{}, generation: randomIRC()}
	s.nick = secret.Nick
	if s.nick == "" {
		s.nick = "km" + randomIRC()
	}
	if s.generation == "" || !ircNick(s.nick) {
		conn.Close()
		return nil, errTransport
	}
	deadline, _ := bounded.Deadline()
	conn.SetDeadline(deadline)
	// Interrupt a blocked fixture/network read promptly if the caller cancels.
	stop := context.AfterFunc(bounded, func() { conn.Close() })
	defer stop()
	if err := s.handshake(bounded, secret, authority); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	s.alive = true
	s.wg.Add(1)
	go s.run()
	a.states[string(b.ID)] = s
	return s, nil
}

func (s *ircSession) write(ctx context.Context, line string) error {
	if strings.ContainsAny(line, "\r\n\x00") || len(line)+2 > 512 || !utf8.ValidString(line) {
		return errBinding
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if ctx.Err() != nil {
		return errTransport
	}
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	s.conn.SetWriteDeadline(deadline)
	_, err := io.WriteString(s.conn, line+"\r\n")
	s.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		return errTransport
	}
	return nil
}

func (s *ircSession) read() (ircMessage, error) {
	b, err := s.reader.ReadSlice('\n')
	if err != nil || len(b) < 2 || b[len(b)-2] != '\r' {
		return ircMessage{}, errTransport
	}
	return parseIRC(string(b[:len(b)-2]))
}

func parseIRC(line string) (ircMessage, error) {
	m := ircMessage{Tags: map[string]string{}}
	if !utf8.ValidString(line) || strings.ContainsAny(line, "\x00\r\n") {
		return m, errTransport
	}
	if strings.HasPrefix(line, "@") {
		tags, rest, ok := strings.Cut(line, " ")
		if !ok || len(tags) > 8192 {
			return m, errTransport
		}
		line = rest
		for _, tag := range strings.Split(tags[1:], ";") {
			key, value, _ := strings.Cut(tag, "=")
			if key == "" || len(m.Tags) >= 128 {
				return m, errTransport
			}
			if _, exists := m.Tags[key]; exists {
				return m, errTransport
			}
			m.Tags[key] = ircUnescape(value)
		}
	}
	if len(line)+2 > 512 {
		return m, errTransport
	}
	if strings.HasPrefix(line, ":") {
		prefix, rest, ok := strings.Cut(line, " ")
		if !ok {
			return m, errTransport
		}
		m.Prefix = prefix[1:]
		line = rest
	}
	command, rest, more := strings.Cut(line, " ")
	m.Command = strings.ToUpper(command)
	if m.Command == "" {
		return m, errTransport
	}
	for more {
		rest = strings.TrimLeft(rest, " ")
		if rest == "" {
			break
		}
		if len(m.Params) >= 15 {
			return m, errTransport
		}
		if rest[0] == ':' {
			m.Params = append(m.Params, rest[1:])
			break
		}
		var p string
		p, rest, more = strings.Cut(rest, " ")
		m.Params = append(m.Params, p)
	}
	return m, nil
}

func ircUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			break
		}
		switch s[i] {
		case ':':
			b.WriteByte(';')
		case 's':
			b.WriteByte(' ')
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

var ircCaps = []string{"sasl", "account-tag", "message-tags", "echo-message", "labeled-response", "batch", "server-time"}

func (s *ircSession) handshake(ctx context.Context, secret ircSecret, authority string) error {
	for _, line := range []string{"CAP LS 302", "NICK " + s.nick, "USER keelmux 0 * :Keelmux channel adapter"} {
		if s.write(ctx, line) != nil {
			return errTransport
		}
	}
	available := map[string]string{}
	acked := map[string]bool{}
	requested, authStarted, authSent, success, welcome, joinSent, joined := false, false, false, false, false, false, false
	account := ""
	for count := 0; count < 512; count++ {
		m, err := s.read()
		if err != nil {
			return errTransport
		}
		if m.Command == "PING" && len(m.Params) == 1 {
			if s.write(ctx, "PONG :"+m.Params[0]) != nil {
				return errTransport
			}
			continue
		}
		switch m.Command {
		case "CAP":
			if len(m.Params) < 3 {
				return errBinding
			}
			sub := m.Params[1]
			caps := m.Params[len(m.Params)-1]
			if sub == "LS" {
				if requested {
					return errBinding
				}
				for _, cap := range strings.Fields(caps) {
					name, value, _ := strings.Cut(cap, "=")
					if len(available) >= 256 {
						return errBinding
					}
					available[name] = value
				}
				if len(m.Params) == 4 && m.Params[2] == "*" {
					continue
				}
				for _, cap := range ircCaps {
					if _, ok := available[cap]; !ok {
						return errBinding
					}
				}
				if mechanisms := available["sasl"]; mechanisms != "" {
					plain := false
					for _, mechanism := range strings.Split(mechanisms, ",") {
						plain = plain || mechanism == "PLAIN"
					}
					if !plain {
						return errBinding
					}
				}
				request := append([]string{}, ircCaps...)
				if _, ok := available["account-notify"]; ok {
					request = append(request, "account-notify")
				}
				if s.write(ctx, "CAP REQ :"+strings.Join(request, " ")) != nil {
					return errTransport
				}
				requested = true
			} else if sub == "NAK" {
				return errBinding
			} else if sub == "ACK" {
				if !requested {
					return errBinding
				}
				for _, cap := range strings.Fields(caps) {
					name, _, _ := strings.Cut(cap, "=")
					acked[name] = true
				}
				complete := true
				for _, cap := range ircCaps {
					complete = complete && acked[cap]
				}
				if complete && !authStarted {
					if s.write(ctx, "AUTHENTICATE PLAIN") != nil {
						return errTransport
					}
					authStarted = true
				}
			}
		case "AUTHENTICATE":
			if !authStarted || authSent || len(m.Params) != 1 || m.Params[0] != "+" {
				return errBinding
			}
			payload := base64.StdEncoding.EncodeToString([]byte(secret.Account + "\x00" + secret.Account + "\x00" + secret.Password))
			for len(payload) > 0 {
				n := min(len(payload), 400)
				chunk := payload[:n]
				payload = payload[n:]
				if s.write(ctx, "AUTHENTICATE "+chunk) != nil {
					return errTransport
				}
				if len(payload) == 0 && n == 400 {
					if s.write(ctx, "AUTHENTICATE +") != nil {
						return errTransport
					}
				}
			}
			authSent = true
		case "900":
			if !authSent || len(m.Params) < 3 || !matchIdentity(s.binding, authority, m.Params[2]) {
				return errBinding
			}
			account = m.Params[2]
		case "903":
			if !authSent || account == "" || success {
				return errBinding
			}
			success = true
			if s.write(ctx, "CAP END") != nil {
				return errTransport
			}
		case "001":
			if !success || len(m.Params) < 1 || !ircNick(m.Params[0]) {
				return errBinding
			}
			s.nick = m.Params[0]
			welcome = true
		case "JOIN":
			if !joinSent || len(m.Params) < 1 || !sameIRCChannel(m.Params[0], s.binding.ChannelID) {
				break
			}
			nick, _, _ := strings.Cut(m.Prefix, "!")
			if strings.EqualFold(nick, s.nick) {
				if tag := m.Tags["account"]; tag != "" && tag != account {
					return errBinding
				}
				joined = true
			}
		case "904", "905", "906", "907", "901", "433", "432", "461", "ERROR", "403", "405", "471", "473", "474", "475", "476", "477", "489":
			return errBinding
		}
		if welcome && !joinSent {
			if s.write(ctx, "JOIN "+s.binding.ChannelID) != nil {
				return errTransport
			}
			joinSent = true
		}
		if joined {
			s.proof = evidence(s.binding, authority, account, "irc_tls_sasl900_account_tag_live_server_ack")
			return nil
		}
	}
	return errTransport
}

func (s *ircSession) terminate() {
	s.mu.Lock()
	if s.alive {
		s.alive = false
	}
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.mu.Unlock()
	s.conn.Close()
}

func (s *ircSession) run() {
	defer s.wg.Done()
	defer s.terminate()
	for {
		m, err := s.read()
		if err != nil {
			return
		}
		if m.Command == "PING" && len(m.Params) == 1 {
			if s.write(context.Background(), "PONG :"+m.Params[0]) != nil {
				return
			}
			continue
		}
		if !s.receive(m) {
			return
		}
	}
}

func (s *ircSession) remember(p *ircPending, r channels.SendResult) {
	if _, ok := s.receipts[p.ID]; !ok {
		s.order = append(s.order, p.ID)
	}
	s.receipts[p.ID] = ircReceipt{Hash: p.Hash, Text: p.Text, Result: r}
	if len(s.order) > ircBufferSize {
		delete(s.receipts, s.order[0])
		s.order = s.order[1:]
	}
	select {
	case p.Result <- r:
	default:
	}
}

func (s *ircSession) finish(label string, r channels.SendResult) {
	if p := s.pending[label]; p != nil {
		s.remember(p, r)
		delete(s.pending, label)
	}
}

func (s *ircSession) receive(m ircMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	nick, _, _ := strings.Cut(m.Prefix, "!")
	if m.Command == "ERROR" || m.Command == "901" || m.Command == "ACCOUNT" && strings.EqualFold(nick, s.nick) && (len(m.Params) != 1 || m.Params[0] != s.proof.SelfID) || m.Command == "PART" && strings.EqualFold(nick, s.nick) && len(m.Params) > 0 && sameIRCChannel(m.Params[0], s.binding.ChannelID) || m.Command == "KICK" && len(m.Params) > 1 && sameIRCChannel(m.Params[0], s.binding.ChannelID) && strings.EqualFold(m.Params[1], s.nick) {
		return false
	}
	if m.Command == "900" && (len(m.Params) < 3 || m.Params[2] != s.proof.SelfID) {
		return false
	}
	if m.Command == "NICK" && strings.EqualFold(nick, s.nick) {
		if len(m.Params) != 1 || !ircNick(m.Params[0]) {
			return false
		}
		s.nick = m.Params[0]
	}
	label := m.Tags["label"]
	if m.Command == "BATCH" {
		if len(m.Params) < 1 || len(m.Params[0]) < 2 {
			return false
		}
		id := m.Params[0][1:]
		if m.Params[0][0] == '+' {
			if len(s.batches) >= 64 {
				return false
			}
			if _, exists := s.batches[id]; exists {
				return false
			}
			if label != "" && s.pending[label] != nil && len(m.Params) == 2 && m.Params[1] == "labeled-response" && m.Tags["batch"] == "" {
				s.batches[id] = &ircBatch{Label: label}
			} else {
				s.batches[id] = &ircBatch{Invalid: true}
			}
		} else if m.Params[0][0] == '-' {
			batch := s.batches[id]
			delete(s.batches, id)
			if batch != nil && batch.Label != "" {
				r := unconfirmed(s.proof)
				if !batch.Invalid && batch.Candidate != nil {
					r = *batch.Candidate
				}
				s.finish(batch.Label, r)
			}
		} else {
			return false
		}
		return true
	}
	var batch *ircBatch
	if id := m.Tags["batch"]; id != "" {
		batch = s.batches[id]
		if batch == nil || batch.Invalid || label != "" {
			return true
		}
		label = batch.Label
	}
	if p := s.pending[label]; label != "" && p != nil {
		r := unconfirmed(s.proof)
		if m.Command == "PRIVMSG" && len(m.Params) == 2 && sameIRCChannel(m.Params[0], s.binding.ChannelID) && m.Params[1] == p.Text && m.Tags["account"] == s.proof.SelfID && channels.ValidID(m.Tags["msgid"]) {
			r = channels.SendResult{State: channels.Delivered, RemoteID: m.Tags["msgid"], Evidence: s.proof}
		} else if m.Command == "401" || m.Command == "403" || m.Command == "404" || m.Command == "442" || m.Command == "482" {
			r = failed()
			r.Evidence = s.proof
		}
		if batch != nil {
			if batch.Candidate != nil {
				batch.Invalid = true
			}
			batch.Candidate = &r
		} else {
			s.finish(label, r)
		}
		return true
	}
	// Unknown/batched history is not a live-source receipt. Client-only account
	// tags, nicknames and messages without server source IDs grant no authority.
	if label != "" || m.Tags["batch"] != "" || m.Command != "PRIVMSG" || len(m.Params) != 2 || !sameIRCChannel(m.Params[0], s.binding.ChannelID) {
		return true
	}
	account, id := m.Tags["account"], m.Tags["msgid"]
	if !channels.ValidID(account) || account == "*" || !channels.ValidID(id) || !channels.ValidText(m.Params[1]) {
		return true
	}
	at, err := time.Parse(time.RFC3339Nano, m.Tags["time"])
	if err != nil || at.IsZero() || at.UnixMilli() <= 0 {
		return true
	}
	at = at.UTC()
	s.sequence++
	s.buffer = append(s.buffer, ircBuffered{Sequence: s.sequence, Message: channels.RemoteMessage{ID: id, ActorID: account, RealmID: s.proof.RealmID, ChannelID: s.binding.ChannelID, Text: m.Params[1], CreatedAt: at, Historical: !at.After(s.binding.ActivationCutoff)}})
	if len(s.buffer) > ircBufferSize {
		s.buffer = s.buffer[1:]
	}
	return true
}

func (a *IRC) Probe(ctx context.Context, b channels.Binding) (channels.Evidence, error) {
	s, err := a.session(ctx, b)
	if err != nil {
		return channels.Evidence{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.alive {
		return channels.Evidence{}, errTransport
	}
	return s.proof, nil
}

func (a *IRC) Fetch(ctx context.Context, b channels.Binding, cursor string, limit int) (channels.FetchResult, error) {
	if !b.Active || b.RealmID == "" || b.SelfID == "" || b.ActivationCutoff.IsZero() || !boundPage(cursor, limit) {
		return channels.FetchResult{}, errBinding
	}
	s, err := a.session(ctx, b)
	if err != nil {
		return channels.FetchResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	position := ircCursor{Binding: string(b.ID), Generation: s.generation}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || !validJSON(raw) || json.Unmarshal(raw, &position) != nil || position.Binding != string(b.ID) {
			return channels.FetchResult{}, errBinding
		}
	}
	if position.Generation != s.generation || position.Sequence > s.sequence || len(s.buffer) > 0 && position.Sequence+1 < s.buffer[0].Sequence {
		return channels.FetchResult{}, channels.ErrHistoryGap
	}
	result := channels.FetchResult{Evidence: s.proof, Messages: []channels.RemoteMessage{}}
	for _, entry := range s.buffer {
		if entry.Sequence <= position.Sequence {
			continue
		}
		if len(result.Messages) == limit {
			result.HasMore = true
			break
		}
		result.Messages = append(result.Messages, entry.Message)
		position.Sequence = entry.Sequence
	}
	raw, _ := json.Marshal(position)
	result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	return result, nil
}

func (a *IRC) Send(ctx context.Context, b channels.Binding, d channels.Delivery, m channels.Message) (channels.SendResult, error) {
	if !sendInput(b, d, m) || strings.ContainsAny(m.Text, "\r\n\x00") || len("PRIVMSG "+b.ChannelID+" :"+m.Text)+2 > 512 {
		return failed(), nil
	}
	s, err := a.session(ctx, b)
	if err != nil || ctx.Err() != nil {
		return failed(), nil
	}
	digest := sha256.Sum256([]byte(string(b.ID) + "\x00" + string(d.ID) + "\x00" + hex.EncodeToString(d.PayloadHash[:])))
	label := hex.EncodeToString(digest[:])
	p := &ircPending{ID: string(d.ID), Hash: d.PayloadHash, Text: m.Text, Result: make(chan channels.SendResult, 1)}
	s.mu.Lock()
	if receipt, ok := s.receipts[p.ID]; ok {
		s.mu.Unlock()
		if receipt.Hash != p.Hash || receipt.Text != p.Text {
			return unconfirmed(s.proof), nil
		}
		return receipt.Result, nil
	}
	if _, ok := s.pending[label]; ok {
		s.mu.Unlock()
		return unconfirmed(s.proof), nil
	}
	for _, admitted := range s.pending {
		if admitted.ID == p.ID {
			s.mu.Unlock()
			return unconfirmed(s.proof), nil
		}
	}
	if !s.alive || len(s.pending) >= 64 {
		s.mu.Unlock()
		return failed(), nil
	}
	s.pending[label] = p
	s.mu.Unlock()
	// Tags have a separate limit. The untagged PRIVMSG body was bounded above.
	if s.writeTagged(ctx, "@label="+label, "PRIVMSG "+b.ChannelID+" :"+m.Text) != nil {
		s.terminate()
		return uncertain(s.proof), nil
	}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case result := <-p.Result:
		return result, nil
	case <-ctx.Done():
		return uncertain(s.proof), nil
	case <-timer.C:
		return uncertain(s.proof), nil
	case <-s.done:
		return uncertain(s.proof), nil
	}
}

func (s *ircSession) writeTagged(ctx context.Context, tags, line string) error {
	// The IRCv3 tags prefix does not consume the ordinary 512-byte limit.
	if len(tags) > 4095 || len(line)+2 > 512 || strings.ContainsAny(tags+line, "\r\n\x00") {
		return errBinding
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if ctx.Err() != nil {
		return errTransport
	}
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	s.conn.SetWriteDeadline(deadline)
	_, err := io.WriteString(s.conn, tags+" "+line+"\r\n")
	s.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		return errTransport
	}
	return nil
}

func (a *IRC) Reconcile(ctx context.Context, b channels.Binding, d channels.Delivery, m channels.Message) (channels.SendResult, error) {
	if !receiptInput(b, d, m) {
		return unconfirmed(channels.Evidence{}), nil
	}
	if _, err := ircEndpoint(b); err != nil {
		return unconfirmed(channels.Evidence{}), nil
	}
	_, hash, err := loadIRC(b.CredentialRef)
	if err != nil || ctx.Err() != nil {
		return unconfirmed(channels.Evidence{}), nil
	}
	// IRC has no external history here. Reconciliation does not create a fresh
	// login/JOIN merely to rediscover that a disconnected echo is unrecoverable.
	a.mu.Lock()
	s := a.states[string(b.ID)]
	a.mu.Unlock()
	if s == nil {
		return channels.SendResult{State: channels.Uncertain, Code: "unsupported"}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.alive || s.secretHash != hash || !s.proof.Matches(b) || s.binding.CredentialRef != b.CredentialRef {
		return channels.SendResult{State: channels.Uncertain, Code: "unsupported"}, nil
	}
	if receipt, ok := s.receipts[string(d.ID)]; ok && receipt.Hash == d.PayloadHash && receipt.Text == m.Text && (d.RemoteID == "" || d.RemoteID == receipt.Result.RemoteID) {
		return receipt.Result, nil
	}
	// No CHATHISTORY capability is requested, and a lost echo is never replayed.
	return channels.SendResult{State: channels.Uncertain, Code: "unsupported", Evidence: s.proof}, nil
}

func (a *IRC) Close() error {
	a.mu.Lock()
	a.closed = true
	states := a.states
	a.states = map[string]*ircSession{}
	a.mu.Unlock()
	for _, s := range states {
		s.terminate()
	}
	for _, s := range states {
		s.wg.Wait()
	}
	return nil
}
