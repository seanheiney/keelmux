package chatadapter

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
)

type ircFixture struct {
	t                                        *testing.T
	mu                                       sync.Mutex
	writeMu                                  sync.Mutex
	wg                                       sync.WaitGroup
	connections                              []net.Conn
	dials, posts, chunks                     int
	nick, label, text                        string
	mode, missingCap, wrongAccount, password string
	ended, authenticated                     bool
}

func newIRCFixture(t *testing.T) (*ircFixture, *IRC, channels.Binding) {
	t.Helper()
	b := testBinding(t, channels.IRC)
	b.Endpoint = "ircs://irc.example:6697"
	b.RealmID = "irc.example:6697"
	b.ChannelID = "#room"
	b.RootID = ""
	secret, _ := json.Marshal(ircSecret{Account: "self", Password: "fixture-password", Nick: "fixturebot"})
	os.WriteFile(b.CredentialRef, secret, 0600)
	f := &ircFixture{t: t, password: "fixture-password"}
	a := NewIRCWithDialer(func(ctx context.Context, authority string) (net.Conn, error) {
		if authority != "irc.example:6697" {
			t.Error("wrong network authority")
		}
		client, server := net.Pipe()
		f.mu.Lock()
		f.dials++
		f.connections = append(f.connections, server)
		f.mu.Unlock()
		f.wg.Add(1)
		go f.serve(server)
		return client, nil
	})
	t.Cleanup(func() {
		a.Close()
		f.mu.Lock()
		for _, c := range f.connections {
			c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f, a, b
}

func (f *ircFixture) postCount() int  { f.mu.Lock(); defer f.mu.Unlock(); return f.posts }
func (f *ircFixture) dialCount() int  { f.mu.Lock(); defer f.mu.Unlock(); return f.dials }
func (f *ircFixture) chunkCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.chunks }
func (f *ircFixture) hasAuth() bool   { f.mu.Lock(); defer f.mu.Unlock(); return f.authenticated }
func (f *ircFixture) hasEnded() bool  { f.mu.Lock(); defer f.mu.Unlock(); return f.ended }

func (f *ircFixture) write(conn net.Conn, lines ...string) bool {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})
	for _, line := range lines {
		if _, err := io.WriteString(conn, line+"\r\n"); err != nil {
			return false
		}
	}
	return true
}

func (f *ircFixture) push(lines ...string) {
	f.t.Helper()
	f.mu.Lock()
	conn := f.connections[len(f.connections)-1]
	f.mu.Unlock()
	if !f.write(conn, lines...) {
		f.t.Fatal("owned protocol fixture could not write")
	}
}

func (f *ircFixture) serve(conn net.Conn) {
	defer f.wg.Done()
	defer conn.Close()
	reader := bufio.NewReaderSize(conn, 8706)
	payload := ""
	nick := ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		m, err := parseIRC(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		if err != nil {
			return
		}
		f.mu.Lock()
		mode, missing, wrong, password := f.mode, f.missingCap, f.wrongAccount, f.password
		f.mu.Unlock()
		switch m.Command {
		case "NICK":
			if len(m.Params) == 1 {
				nick = m.Params[0]
				f.mu.Lock()
				f.nick = nick
				f.mu.Unlock()
			}
		case "USER":
			caps := []string{}
			for _, cap := range ircCaps {
				if cap != missing {
					caps = append(caps, cap)
				}
			}
			caps = append(caps, "account-notify")
			if !f.write(conn, ":server CAP * LS :"+strings.Join(caps, " ")) {
				return
			}
		case "CAP":
			if len(m.Params) == 2 && m.Params[0] == "REQ" {
				if !f.write(conn, ":server CAP * ACK :"+m.Params[1]) {
					return
				}
			}
			if len(m.Params) == 1 && m.Params[0] == "END" {
				f.mu.Lock()
				f.ended = true
				f.mu.Unlock()
				if !f.write(conn, ":server 001 "+nick+" :Welcome") {
					return
				}
			}
		case "AUTHENTICATE":
			if len(m.Params) != 1 {
				return
			}
			chunk := m.Params[0]
			if chunk == "PLAIN" {
				if !f.write(conn, "AUTHENTICATE +") {
					return
				}
				continue
			}
			f.mu.Lock()
			f.chunks++
			f.mu.Unlock()
			if chunk != "+" {
				payload += chunk
			}
			if len(chunk) == 400 {
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(payload)
			if err != nil || string(decoded) != "self\x00self\x00"+password {
				f.t.Error("SASL fixture received incorrect credential framing")
				return
			}
			f.mu.Lock()
			f.authenticated = true
			f.mu.Unlock()
			account := "self"
			if wrong != "" {
				account = wrong
			}
			if mode == "sasl-failed" {
				f.write(conn, ":server 904 "+nick+" :SASL authentication failed")
				return
			}
			if !f.write(conn, ":server 900 "+nick+" "+nick+"!u@h "+account+" :Logged in", ":server 903 "+nick+" :SASL authentication successful") {
				return
			}
		case "JOIN":
			wireNick, wireChannel := nick, m.Params[0]
			if mode == "wire-case" {
				wireNick = strings.ToUpper(nick)
				wireChannel = strings.ToUpper(wireChannel)
			}
			if !f.write(conn, "@account=self :"+wireNick+"!u@h JOIN "+wireChannel) {
				return
			}
		case "PRIVMSG":
			if len(m.Params) != 2 {
				return
			}
			label := m.Tags["label"]
			f.mu.Lock()
			f.posts++
			f.label = label
			f.text = m.Params[1]
			f.mu.Unlock()
			if mode == "drop" {
				return
			}
			if mode == "late" {
				continue
			}
			target, account := m.Params[0], "self"
			if mode == "wire-case" {
				target = strings.ToUpper(target)
			}
			if mode == "wrong-target" {
				target = "#foreign"
			}
			if mode == "wrong-self" {
				account = "other"
			}
			echo := "@label=" + label + ";account=" + account + ";msgid=source-reply :" + nick + "!u@h PRIVMSG " + target + " :" + m.Params[1]
			if mode == "no-msgid" {
				echo = "@label=" + label + ";account=self :" + nick + "!u@h PRIVMSG " + target + " :" + m.Params[1]
			}
			if mode == "rejected" {
				echo = "@label=" + label + " :server 404 " + nick + " #room :Cannot send"
			}
			if mode == "batch" || mode == "incomplete-batch" {
				body := "@batch=b1;account=self;msgid=source-reply :" + nick + "!u@h PRIVMSG " + target + " :" + m.Params[1]
				if !f.write(conn, "@label="+label+" :server BATCH +b1 labeled-response", body) {
					return
				}
				if mode == "batch" {
					if !f.write(conn, ":server BATCH -b1") {
						return
					}
				}
				continue
			}
			if !f.write(conn, echo) {
				return
			}
		case "PONG":
		default:
		}
	}
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("owned protocol fixture condition did not complete")
}

func TestIRCPersistentAuthLiveInboxUsesAccountNotNick(t *testing.T) {
	f, a, b := newIRCFixture(t)
	discovery := b
	discovery.RealmID = ""
	discovery.SelfID = ""
	e, err := a.Probe(context.Background(), discovery)
	if err != nil || !e.Matches(b) || !f.hasAuth() || !f.hasEnded() {
		t.Fatal("authenticated membership probe", err)
	}
	f.push("@account=alice;msgid=live1;time=2023-11-14T22:13:21Z :fixturebot!spoof@host PRIVMSG #room :Unicode 世界 🧭",
		"@msgid=missing-account :alice!u@h PRIVMSG #room :No account authority",
		"@+account=alice;msgid=client-tag :alice!u@h PRIVMSG #room :Client tag is not authority",
		"@account=alice :alice!u@h PRIVMSG #room :No stable source ID",
		"@account=alice;msgid=missing-time :alice!u@h PRIVMSG #room :No authoritative timestamp",
		"@account=alice;msgid=bad-time;time=invalid :alice!u@h PRIVMSG #room :Invalid authoritative timestamp",
		"@account=alice;msgid=foreign :alice!u@h PRIVMSG #foreign :Wrong channel")
	var result channels.FetchResult
	eventually(t, func() bool {
		result, err = a.Fetch(context.Background(), b, "", 10)
		return err == nil && len(result.Messages) == 1
	})
	if result.Messages[0].ID != "live1" || result.Messages[0].ActorID != "alice" || !result.Messages[0].Matches(b) || f.dialCount() != 1 {
		t.Fatal("nick/client tag/wrong channel became authenticated author")
	}
	page, err := a.Fetch(context.Background(), b, result.NextCursor, 10)
	if err != nil || len(page.Messages) != 0 || f.dialCount() != 1 {
		t.Fatal("Fetch reconnects or repeats live intake", err)
	}
	f.push("PING :owned-ping")
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Probe(context.Background(), b); err == nil {
		t.Fatal("closed adapter reopened network")
	}
}

func TestIRCSASLFramingAndPreflightIdentityFailures(t *testing.T) {
	t.Run("exact400-terminator", func(t *testing.T) {
		f, a, b := newIRCFixture(t)
		password := strings.Repeat("p", 290)
		secret, _ := json.Marshal(ircSecret{Account: "self", Password: password, Nick: "fixturebot"})
		os.WriteFile(b.CredentialRef, secret, 0600)
		f.password = password
		if _, err := a.Probe(context.Background(), b); err != nil || f.chunkCount() != 2 {
			t.Fatal("SASL 400-byte terminal chunk", err)
		}
	})
	for _, name := range []string{"account-tag", "sasl", "server-time", "wrong-account", "sasl-failed"} {
		t.Run(name, func(t *testing.T) {
			f, a, b := newIRCFixture(t)
			if name == "wrong-account" {
				f.wrongAccount = "foreign"
			} else if name == "sasl-failed" {
				f.mode = name
			} else {
				f.missingCap = name
			}
			d, m := testDelivery(b)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r, err := a.Send(ctx, b, d, m)
			if err != nil || r.State != channels.Failed || r.Evidence.Source != "" || f.postCount() != 0 {
				t.Fatal("SASL/capability failure emitted message", err)
			}
		})
	}
}

func TestIRCQualifiedEchoAcknowledgementAndBatchCompletion(t *testing.T) {
	for _, mode := range []string{"normal", "batch", "wrong-target", "wrong-self", "no-msgid", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			f, a, b := newIRCFixture(t)
			f.mode = mode
			d, m := testDelivery(b)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r, err := a.Send(ctx, b, d, m)
			want := channels.Uncertain
			if mode == "normal" || mode == "batch" {
				want = channels.Delivered
			}
			if mode == "rejected" {
				want = channels.Failed
			}
			if err != nil || r.State != want || f.postCount() != 1 || !r.Evidence.Matches(b) {
				t.Fatalf("state=%s want=%s posts=%d err=%v", r.State, want, f.postCount(), err)
			}
			if want == channels.Delivered {
				r, _ = a.Reconcile(ctx, b, d, m)
				if r.State != want || f.postCount() != 1 || r.RemoteID != "source-reply" {
					t.Fatal("reconcile repeated PRIVMSG")
				}
				r, _ = a.Send(ctx, b, d, m)
				if r.State != want || f.postCount() != 1 {
					t.Fatal("same delivery sent twice on retained connection")
				}
			}
		})
	}
}

func TestIRCLostEchoNeverReplaysAndLateBatchCanReconcile(t *testing.T) {
	for _, mode := range []string{"drop", "late", "incomplete-batch"} {
		t.Run(mode, func(t *testing.T) {
			f, a, b := newIRCFixture(t)
			f.mode = mode
			d, m := testDelivery(b)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			r, _ := a.Send(ctx, b, d, m)
			if r.State != channels.Uncertain || f.postCount() != 1 {
				t.Fatal("lost echo was definitive")
			}
			fresh, cancelFresh := context.WithTimeout(context.Background(), time.Second)
			defer cancelFresh()
			r, _ = a.Reconcile(fresh, b, d, m)
			if r.State != channels.Uncertain || f.postCount() != 1 {
				t.Fatal("read-only reconciliation resent message")
			}
			if mode == "late" {
				f.mu.Lock()
				label, text, nick := f.label, f.text, f.nick
				f.mu.Unlock()
				f.push("@label=" + label + ";account=self;msgid=source-reply :" + nick + "!u@h PRIVMSG #room :" + text)
			}
			if mode == "incomplete-batch" {
				f.push(":server BATCH -b1")
			}
			if mode != "drop" {
				eventually(t, func() bool { r, _ = a.Reconcile(fresh, b, d, m); return r.State == channels.Delivered })
				if f.postCount() != 1 {
					t.Fatal("late exact ACK caused replay")
				}
			}
		})
	}
}

func TestIRCBoundedBufferSignalsGapAndReconnectionEpoch(t *testing.T) {
	f, a, b := newIRCFixture(t)
	if _, err := a.Probe(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	first, err := a.Fetch(context.Background(), b, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < ircBufferSize+1; i++ {
		f.push(fmt.Sprintf("@account=alice;msgid=live%d;time=2023-11-14T22:13:21Z :alice!u@h PRIVMSG #room :authored %d", i, i))
	}
	eventually(t, func() bool {
		_, err = a.Fetch(context.Background(), b, first.NextCursor, 1)
		return errors.Is(err, channels.ErrHistoryGap)
	})
	f.mu.Lock()
	conn := f.connections[len(f.connections)-1]
	f.mu.Unlock()
	conn.Close()
	eventually(t, func() bool {
		a.mu.Lock()
		s := a.states[string(b.ID)]
		a.mu.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.alive
	})
	_, err = a.Fetch(context.Background(), b, first.NextCursor, 1)
	if !errors.Is(err, channels.ErrHistoryGap) || f.dialCount() != 2 {
		t.Fatal("old cursor silently pretended replay after disconnect", err)
	}
}

func TestIRCCredentialRotationRefusesWrongSelfBeforeSend(t *testing.T) {
	f, a, b := newIRCFixture(t)
	if _, err := a.Probe(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.wrongAccount = "different-self"
	f.mu.Unlock()
	secret, _ := json.Marshal(ircSecret{Account: "self", Password: "fixture-password", Nick: "rotatedbot"})
	os.WriteFile(b.CredentialRef, secret, 0600)
	d, m := testDelivery(b)
	r, _ := a.Send(context.Background(), b, d, m)
	if r.State != channels.Failed || f.postCount() != 0 || f.dialCount() != 2 {
		t.Fatal("rotated secret switched immutable services identity")
	}
}

func TestIRCDisabledHistoricalReceiptDoesNotSendOrTrustChangedText(t *testing.T) {
	f, a, b := newIRCFixture(t)
	d, m := testDelivery(b)
	r, _ := a.Send(context.Background(), b, d, m)
	if r.State != channels.Delivered {
		t.Fatal("initial echo proof")
	}
	b.Active = false
	r, _ = a.Send(context.Background(), b, d, m)
	if r.State != channels.Failed || f.postCount() != 1 {
		t.Fatal("disabled IRC binding sent")
	}
	r, _ = a.Reconcile(context.Background(), b, d, m)
	if r.State != channels.Delivered || f.postCount() != 1 {
		t.Fatal("disabled retained readonly receipt did not resolve")
	}
	m.Text = "changed authored text"
	r, _ = a.Reconcile(context.Background(), b, d, m)
	if r.State != channels.Uncertain || f.postCount() != 1 {
		t.Fatal("changed payload trusted old receipt")
	}
}

func TestIRCDuplicateSourcePreservesTimestampAndPendingIDNeverRepeats(t *testing.T) {
	f, a, b := newIRCFixture(t)
	if _, err := a.Probe(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	line := "@account=alice;msgid=stable-source;time=2023-11-14T22:13:21.123456Z :alice!u@h PRIVMSG #room :authored content"
	f.push(line, line)
	var page channels.FetchResult
	eventually(t, func() bool { page, _ = a.Fetch(context.Background(), b, "", 10); return len(page.Messages) == 2 })
	if page.Messages[0] != page.Messages[1] {
		t.Fatal("same source got different identity/timestamp")
	}
	f.mu.Lock()
	f.mode = "late"
	f.mu.Unlock()
	d, m := testDelivery(b)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r, _ := a.Send(ctx, b, d, m)
	if r.State != channels.Uncertain || f.postCount() != 1 {
		t.Fatal("first attempted send")
	}
	m.Text = "changed payload under same stable ID"
	d.PayloadHash[0] ^= 1
	r, _ = a.Send(context.Background(), b, d, m)
	if r.State != channels.Uncertain || f.postCount() != 1 {
		t.Fatal("pending stable delivery ID repeated a PRIVMSG")
	}
}

func TestIRCWireBoundsTLSCanonicalizationAndInjection(t *testing.T) {
	f, a, b := newIRCFixture(t)
	d, m := testDelivery(b)
	for _, text := range []string{"line\r\nPRIVMSG #foreign :injected", strings.Repeat("世", 200)} {
		m.Text = text
		r, _ := a.Send(context.Background(), b, d, m)
		if r.State != channels.Failed || f.dialCount() != 0 || f.postCount() != 0 {
			t.Fatal("invalid wire text reached transport")
		}
	}
	for _, line := range []string{"@account=alice;account=other :n PRIVMSG #room :x", strings.Repeat("x", 511), "@account=alice :n PRIVMSG #room :bad\x00"} {
		if _, err := parseIRC(line); err == nil {
			t.Fatal("ambiguous or oversized IRC line accepted")
		}
	}
	mapped, err := parseIRC("@x=a\\:b\\sc\\\\d\\r\\n :n PRIVMSG #room :Unicode 世界")
	if err != nil || mapped.Tags["x"] != "a;b c\\d\r\n" {
		t.Fatal("IRCv3 tag escapes", err)
	}
	for _, raw := range []string{"irc://irc.example:6667", "ircs://user:password@irc.example:6697", "ircs://irc.example:6697/path"} {
		bad := b
		bad.Endpoint = raw
		if _, err := a.Probe(context.Background(), bad); err == nil {
			t.Fatal("non-TLS/noncanonical IRC endpoint accepted")
		}
	}
	for _, tc := range []struct {
		provider  channels.Provider
		raw, want string
	}{{channels.Mattermost, "https://MM.EXAMPLE/base/", "https://mm.example/base"}, {channels.Slack, "https://SLACK.COM/api/", "https://slack.com/api"}, {channels.IRC, "ircs://IRC.EXAMPLE:6697/", "ircs://irc.example:6697"}} {
		got, err := channels.CanonicalEndpoint(tc.provider, tc.raw)
		if err != nil || got != tc.want {
			t.Fatal("canonical endpoint", got, err)
		}
	}
}

func TestIRCCanonicalRouteAcceptsOnlySafeWireCaseAliases(t *testing.T) {
	f, a, b := newIRCFixture(t)
	f.mode = "wire-case"
	canonical, err := channels.CanonicalChannel(channels.IRC, "#ROOM")
	if err != nil || canonical != b.ChannelID {
		t.Fatal("canonical bind address", err)
	}
	if _, err := a.Probe(context.Background(), b); err != nil {
		t.Fatal("wire case membership", err)
	}
	f.push("@account=Alice;msgid=upper-case;time=2023-11-14T22:13:21Z :alice!u@h PRIVMSG #ROOM :same canonical route")
	var page channels.FetchResult
	eventually(t, func() bool { page, _ = a.Fetch(context.Background(), b, "", 1); return len(page.Messages) == 1 })
	if page.Messages[0].ChannelID != "#room" || page.Messages[0].ActorID != "Alice" {
		t.Fatal("wire alias changed route or services-account identity")
	}
	d, m := testDelivery(b)
	r, _ := a.Send(context.Background(), b, d, m)
	if r.State != channels.Delivered {
		t.Fatal("wire case reply acknowledgement")
	}
	raw := b
	raw.ID = "new-raw-binding"
	raw.ChannelID = "#ROOM"
	if _, err := a.Probe(context.Background(), raw); err == nil {
		t.Fatal("uncanonical raw address accepted as a second route")
	}
	if f.dialCount() != 1 {
		t.Fatal("raw alias opened another route connection")
	}
}
