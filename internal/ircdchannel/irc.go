package ircdchannel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/ircd"
)

type botSecret struct {
	Account  string `json:"account"`
	Password string `json:"password"`
}

func loadSecret(c ircd.Config) (botSecret, error) {
	var secret botSecret
	raw, err := config.ReadPrivateSecret(c.CredentialRef)
	if err != nil {
		return secret, ErrAuthentication
	}
	// Duplicate fields could conceal different credentials from independent
	// readers; the private managed credential has precisely these two keys.
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return secret, ErrAuthentication
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] || name != "account" && name != "password" {
			return botSecret{}, ErrAuthentication
		}
		seen[name] = true
		var value string
		if d.Decode(&value) != nil {
			return botSecret{}, ErrAuthentication
		}
		if name == "account" {
			secret.Account = value
		} else {
			secret.Password = value
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(seen) != 2 {
		return botSecret{}, ErrAuthentication
	}
	if _, err = d.Token(); err != io.EOF || secret.Account != c.Account || len(secret.Password) != 64 {
		return botSecret{}, ErrAuthentication
	}
	for _, ch := range secret.Password {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return botSecret{}, ErrAuthentication
		}
	}
	return secret, nil
}

type message struct {
	tags            map[string]string
	prefix, command string
	params          []string
}

func parse(line string) (message, error) {
	m := message{tags: map[string]string{}}
	if !utf8.ValidString(line) || strings.ContainsAny(line, "\r\n\x00") {
		return m, ErrTransport
	}
	if strings.HasPrefix(line, "@") {
		tags, rest, ok := strings.Cut(line, " ")
		if !ok || len(tags) > 8192 {
			return m, ErrTransport
		}
		for _, tag := range strings.Split(tags[1:], ";") {
			key, value, _ := strings.Cut(tag, "=")
			if key == "" || len(m.tags) >= 128 {
				return m, ErrTransport
			}
			if _, duplicate := m.tags[key]; duplicate {
				return m, ErrTransport
			}
			m.tags[key] = value
		}
		line = rest
	}
	if len(line)+2 > 512 {
		return m, ErrTransport
	}
	if strings.HasPrefix(line, ":") {
		prefix, rest, ok := strings.Cut(line, " ")
		if !ok || len(prefix) < 2 {
			return m, ErrTransport
		}
		m.prefix, line = prefix[1:], rest
	}
	command, rest, more := strings.Cut(line, " ")
	if command == "" {
		return m, ErrTransport
	}
	for _, c := range command {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return m, ErrTransport
		}
	}
	m.command = command
	for more {
		rest = strings.TrimLeft(rest, " ")
		if rest == "" {
			break
		}
		if len(m.params) >= 15 {
			return m, ErrTransport
		}
		if rest[0] == ':' {
			m.params = append(m.params, rest[1:])
			break
		}
		var p string
		p, rest, more = strings.Cut(rest, " ")
		m.params = append(m.params, p)
	}
	return m, nil
}

func read(reader *bufio.Reader) (message, error) {
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) < 2 || line[len(line)-2] != '\r' {
		return message{}, ErrTransport
	}
	return parse(string(line[:len(line)-2]))
}

func write(ctx context.Context, conn net.Conn, line string) error {
	if ctx.Err() != nil {
		return ErrTransport
	}
	if strings.ContainsAny(line, "\r\n\x00") || !utf8.ValidString(line) || len(line)+2 > 512 {
		return ErrScope
	}
	if _, err := io.WriteString(conn, line+"\r\n"); err != nil {
		return ErrTransport
	}
	return nil
}

func numericFor(m message, nick string) bool {
	return m.prefix != "" && !strings.ContainsAny(m.prefix, "!@") && len(m.params) >= 1 && m.params[0] == nick
}

func (s *Service) register(ctx context.Context, c ircd.Config, secret botSecret, channel string) error {
	if ctx.Err() != nil {
		return ErrTransport
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ErrTransport
	}
	nick := "kmc-" + hex.EncodeToString(random[:])
	conn, err := s.runtime.Dial(ctx, c.RealmID)
	if err != nil || conn == nil {
		return ErrTransport
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(s.timeout)
	}
	if conn.SetDeadline(deadline) != nil {
		return ErrTransport
	}
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.Close(); close(closed) })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	reader := bufio.NewReaderSize(conn, 8706)
	if err := authenticate(ctx, conn, reader, nick, secret); err != nil {
		return err
	}
	if err := write(ctx, conn, "JOIN "+channel); err != nil {
		return err
	}
	joined := false
	for count := 0; count < 512; count++ {
		m, err := read(reader)
		if err != nil {
			return err
		}
		if m.command == "PING" {
			if len(m.params) != 1 {
				return ErrTransport
			}
			if err = write(ctx, conn, "PONG :"+m.params[0]); err != nil {
				return err
			}
			continue
		}
		if authenticationChanged(m, nick, secret.Account) {
			return ErrAuthentication
		}
		switch m.command {
		case "JOIN":
			if !joined && len(m.params) >= 1 && m.params[0] == channel {
				who, _, _ := strings.Cut(m.prefix, "!")
				if who == nick {
					if !strings.Contains(m.prefix, "!") || m.tags["account"] != "" && m.tags["account"] != secret.Account {
						return ErrAuthentication
					}
					joined = true
					if err = write(ctx, conn, "PRIVMSG ChanServ :REGISTER "+channel); err != nil {
						return err
					}
				}
			}
		case "NOTICE":
			who, _, _ := strings.Cut(m.prefix, "!")
			if joined && who == "ChanServ" && len(m.params) == 2 && m.params[0] == nick {
				// A services notice is only a synchronization hint, never proof of
				// successful registration. The authenticated API owns that proof.
				return nil
			}
		case "ERROR", "403", "405", "471", "473", "474", "475", "476", "477", "489", "482", "442":
			return ErrTransport
		}
	}
	return ErrTransport
}

func authenticationChanged(m message, nick, account string) bool {
	who, _, _ := strings.Cut(m.prefix, "!")
	return m.command == "901" || m.command == "900" && (!numericFor(m, nick) || len(m.params) < 3 || m.params[2] != account) || who == nick && (m.command == "NICK" || m.command == "ACCOUNT" && (len(m.params) != 1 || m.params[0] != account))
}

func authenticate(ctx context.Context, conn net.Conn, reader *bufio.Reader, nick string, secret botSecret) error {
	for _, line := range []string{"CAP LS 302", "NICK " + nick, "USER keelmux 0 * :Keelmux managed channel"} {
		if err := write(ctx, conn, line); err != nil {
			return err
		}
	}
	available, acked := map[string]string{}, map[string]bool{}
	requested, authStarted, authSent, accountConfirmed, saslConfirmed := false, false, false, false, false
	for count := 0; count < 512; count++ {
		m, err := read(reader)
		if err != nil {
			return err
		}
		if m.command == "PING" {
			if len(m.params) != 1 {
				return ErrTransport
			}
			if err = write(ctx, conn, "PONG :"+m.params[0]); err != nil {
				return err
			}
			continue
		}
		switch m.command {
		case "CAP":
			if len(m.params) < 3 || m.params[0] != "*" && m.params[0] != nick {
				return ErrAuthentication
			}
			sub, caps := m.params[1], m.params[len(m.params)-1]
			if sub == "LS" {
				if requested || len(m.params) != 3 && !(len(m.params) == 4 && m.params[2] == "*") {
					return ErrAuthentication
				}
				for _, token := range strings.Fields(caps) {
					name, value, _ := strings.Cut(token, "=")
					if name == "" || len(available) >= 256 {
						return ErrAuthentication
					}
					if _, duplicate := available[name]; duplicate {
						return ErrAuthentication
					}
					available[name] = value
				}
				if len(m.params) == 4 {
					continue
				}
				mechanisms, sasl := available["sasl"]
				_, accountTag := available["account-tag"]
				plain := mechanisms == ""
				for _, mechanism := range strings.Split(mechanisms, ",") {
					plain = plain || mechanism == "PLAIN"
				}
				if !sasl || !plain || !accountTag {
					return ErrAuthentication
				}
				if err = write(ctx, conn, "CAP REQ :sasl account-tag"); err != nil {
					return err
				}
				requested = true
			} else if sub == "ACK" {
				if !requested || authStarted || len(m.params) != 3 {
					return ErrAuthentication
				}
				for _, token := range strings.Fields(caps) {
					if token != "sasl" && token != "account-tag" || acked[token] {
						return ErrAuthentication
					}
					acked[token] = true
				}
				if acked["sasl"] && acked["account-tag"] {
					if err = write(ctx, conn, "AUTHENTICATE PLAIN"); err != nil {
						return err
					}
					authStarted = true
				}
			} else {
				return ErrAuthentication
			}
		case "AUTHENTICATE":
			if !authStarted || authSent || len(m.params) != 1 || m.params[0] != "+" {
				return ErrAuthentication
			}
			payload := base64.StdEncoding.EncodeToString([]byte(secret.Account + "\x00" + secret.Account + "\x00" + secret.Password))
			for len(payload) > 0 {
				n := min(400, len(payload))
				if err = write(ctx, conn, "AUTHENTICATE "+payload[:n]); err != nil {
					return err
				}
				payload = payload[n:]
				if len(payload) == 0 && n == 400 {
					if err = write(ctx, conn, "AUTHENTICATE +"); err != nil {
						return err
					}
				}
			}
			authSent = true
		case "900":
			if !authSent || accountConfirmed || !numericFor(m, nick) || len(m.params) != 4 || m.params[2] != secret.Account || !strings.HasPrefix(m.params[1], nick+"!") || !strings.Contains(m.params[1], "@") {
				return ErrAuthentication
			}
			accountConfirmed = true
		case "903":
			if !authSent || !accountConfirmed || saslConfirmed || !numericFor(m, nick) {
				return ErrAuthentication
			}
			saslConfirmed = true
			if err = write(ctx, conn, "CAP END"); err != nil {
				return err
			}
		case "001":
			if !saslConfirmed || !numericFor(m, nick) {
				return ErrAuthentication
			}
			return nil
		case "904", "905", "906", "907", "901", "433", "432", "461", "ERROR", "NICK", "ACCOUNT":
			return ErrAuthentication
		}
	}
	return ErrTransport
}
