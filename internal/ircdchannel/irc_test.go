package ircdchannel

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func onlyEOF(conn net.Conn, reader *bufio.Reader) error {
	line, err := reader.ReadString('\n')
	if err == nil || line != "" {
		return errors.New("failed authentication sent another command")
	}
	return nil
}

func TestExact900903AndNickRequiredBeforeJoin(t *testing.T) {
	for _, scenario := range []string{"wrong-account", "missing-900", "wrong-target", "wrong-mask", "duplicate-900", "client-prefix", "missing-903", "changed-welcome-nick", "mixed-placeholder", "mismatched-903-target", "placeholder-wrong-welcome"} {
		t.Run(scenario, func(t *testing.T) {
			r, s := fixture(t)
			r.script = func(conn net.Conn) error {
				reader := bufio.NewReader(conn)
				nick, err := saslExchange(conn, reader)
				if err != nil {
					return err
				}
				valid900 := ":owned 900 " + nick + " " + nick + "!keelmux@localhost keelmux-bot :Logged in"
				var replies []string
				switch scenario {
				case "wrong-account":
					replies = []string{":owned 900 " + nick + " " + nick + "!keelmux@localhost foreign :Logged in"}
				case "missing-900":
					replies = []string{":owned 903 " + nick + " :SASL successful"}
				case "wrong-target":
					replies = []string{":owned 900 other " + nick + "!keelmux@localhost keelmux-bot :Logged in"}
				case "wrong-mask":
					replies = []string{":owned 900 " + nick + " other!keelmux@localhost keelmux-bot :Logged in"}
				case "duplicate-900":
					replies = []string{valid900, valid900}
				case "client-prefix":
					replies = []string{":attacker!user@host 900 " + nick + " " + nick + "!keelmux@localhost keelmux-bot :Logged in"}
				case "missing-903":
					replies = []string{valid900, ":owned 001 " + nick + " :Welcome"}
				case "mixed-placeholder":
					replies = []string{":owned 900 * " + nick + "!keelmux@localhost keelmux-bot :Logged in"}
				case "mismatched-903-target":
					replies = []string{":owned 900 * * keelmux-bot :Logged in", ":owned 903 " + nick + " :SASL successful"}
				case "changed-welcome-nick", "placeholder-wrong-welcome":
					target := nick
					if scenario == "placeholder-wrong-welcome" {
						valid900 = ":owned 900 * * keelmux-bot :Logged in"
						target = "*"
					}
					if err = emit(conn, valid900, ":owned 903 "+target+" :SASL successful"); err != nil {
						return err
					}
					if err = expect(reader, "CAP END"); err != nil {
						return err
					}
					replies = []string{":owned 001 other :Welcome"}
				}
				if err = emit(conn, replies...); err != nil {
					return err
				}
				return onlyEOF(conn, reader)
			}
			thread := domain.NewID("thread")
			if _, err := s.Ensure(context.Background(), thread, domain.NewID("operation")); !errors.Is(err, ErrAuthentication) {
				t.Fatal("invalid authentication proof accepted", err)
			}
			if _, err := s.Ensure(context.Background(), thread, domain.NewID("operation")); !errors.Is(err, ErrUncertain) {
				t.Fatal("auth failed attempt replayed", err)
			}
			if dials, mutations := r.counts(); dials != 1 || mutations != 0 {
				t.Fatal("authentication failure mutated/replayed", dials, mutations)
			}
		})
	}
}

func TestStrictCapabilityNegotiation(t *testing.T) {
	for _, scenario := range []string{"missing-sasl", "missing-account-tag", "unsupported-mechanism", "negative-ack", "unsolicited-auth", "nak"} {
		t.Run(scenario, func(t *testing.T) {
			r, s := fixture(t)
			r.script = func(conn net.Conn) error {
				reader := bufio.NewReader(conn)
				if _, err := startHandshake(conn, reader); err != nil {
					return err
				}
				var err error
				switch scenario {
				case "missing-sasl":
					err = emit(conn, ":owned CAP * LS :account-tag")
				case "missing-account-tag":
					err = emit(conn, ":owned CAP * LS :sasl=PLAIN")
				case "unsupported-mechanism":
					err = emit(conn, ":owned CAP * LS :account-tag sasl=EXTERNAL")
				default:
					if err = emit(conn, ":owned CAP * LS :account-tag sasl=PLAIN"); err != nil {
						return err
					}
					if err = expect(reader, "CAP REQ :sasl account-tag"); err != nil {
						return err
					}
					if scenario == "negative-ack" {
						err = emit(conn, ":owned CAP * ACK :-sasl account-tag")
					}
					if scenario == "unsolicited-auth" {
						err = emit(conn, "AUTHENTICATE +")
					}
					if scenario == "nak" {
						err = emit(conn, ":owned CAP * NAK :sasl account-tag")
					}
				}
				if err != nil {
					return err
				}
				return onlyEOF(conn, reader)
			}
			if _, err := s.Ensure(context.Background(), domain.NewID("thread"), domain.NewID("operation")); !errors.Is(err, ErrAuthentication) {
				t.Fatal("invalid negotiation accepted", err)
			}
		})
	}
}

func TestAccountDriftDuringJoinNeverRegisters(t *testing.T) {
	for _, scenario := range []string{"join-account", "logout", "nick-change"} {
		t.Run(scenario, func(t *testing.T) {
			r, s := fixture(t)
			r.script = func(conn net.Conn) error {
				reader := bufio.NewReader(conn)
				nick, err := handshake(conn, reader)
				if err != nil {
					return err
				}
				r.mu.Lock()
				channel := r.op.Channel
				r.mu.Unlock()
				if err = expect(reader, "JOIN "+channel); err != nil {
					return err
				}
				line := "@account=foreign :" + nick + "!user@host JOIN :" + channel
				if scenario == "logout" {
					line = ":" + nick + "!user@host ACCOUNT *"
				}
				if scenario == "nick-change" {
					line = ":" + nick + "!user@host NICK :other"
				}
				if err = emit(conn, line); err != nil {
					return err
				}
				return onlyEOF(conn, reader)
			}
			if _, err := s.Ensure(context.Background(), domain.NewID("thread"), domain.NewID("operation")); !errors.Is(err, ErrAuthentication) {
				t.Fatal("account drift accepted", err)
			}
		})
	}
}

func TestBoundedParserRejectsAmbiguity(t *testing.T) {
	for _, line := range []string{"@account=a;account=b :owned JOIN #x", strings.Repeat("x", 511), "@" + strings.Repeat("x", 8193) + " :owned JOIN #x", ": JOIN #x", "JOIN #x\r\nPRIVMSG #x :injected", "JOIN #x\x00", "join #x", string([]byte{0xff})} {
		if _, err := parse(line); err == nil {
			t.Fatal("malformed IRC line accepted")
		}
	}
	for _, line := range []string{"PING :token", ":owned CAP * LS * :account-tag", "@account=keelmux-bot :kmc-abc!user@host JOIN :#keelmux-owned"} {
		if _, err := parse(line); err != nil {
			t.Fatal("valid wire line rejected", err)
		}
	}
	reader := bufio.NewReader(strings.NewReader("PING :no-crlf\n"))
	if _, err := read(reader); err == nil {
		t.Fatal("bare newline accepted")
	}
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	if err := write(context.Background(), client, "JOIN #x\r\nPRIVMSG #x :bad"); !errors.Is(err, ErrScope) {
		t.Fatal("wire injection accepted")
	}
}
