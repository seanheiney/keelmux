package ircd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"github.com/seanheiney/keelmux/internal/domain"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type configRecord struct {
	Format                                                         int
	Config                                                         Config
	IRCPort, APIPort                                               int
	ServerName, YAMLSHA256, CASHA256, CertSHA256, KeySHA256, Stage string
}
type saslSecret struct {
	Account  string `json:"account"`
	Password string `json:"password"`
}

func (m *Manager) Init(ctx context.Context, opts InitOptions) (Config, error) {
	unlock, e := m.lock(ctx, true)
	if e != nil {
		return Config{}, e
	}
	defer unlock()
	if r, e := m.readConfig(ctx); e == nil {
		if opts.IRCPort != 0 && opts.IRCPort != r.IRCPort || opts.APIPort != 0 && opts.APIPort != r.APIPort {
			return Config{}, ErrConflict
		}
		if r.Stage != "ready" {
			if _, e = m.command(ctx, r.Config.Runtime.Path, "run", "--conf", filepath.Join(m.dir, "ergo.yaml"), "--smoke", "--quiet"); e != nil {
				return Config{}, ErrRepair
			}
			r.Stage = "ready"
			if writeJSON(filepath.Join(m.dir, "config.json"), r, true) != nil {
				return Config{}, ErrRepair
			}
		}
		return r.Config, nil
	} else if !os.IsNotExist(e) {
		return Config{}, e
	}
	runtime, e := m.download(ctx)
	if e != nil {
		return Config{}, e
	}
	ircPort, e := loopbackPort(opts.IRCPort)
	if e != nil {
		return Config{}, e
	}
	apiPort, e := loopbackPort(opts.APIPort)
	for retries := 0; e == nil && apiPort == ircPort && opts.APIPort == 0 && retries < 8; retries++ {
		apiPort, e = loopbackPort(0)
	}
	if e != nil || ircPort == apiPort {
		return Config{}, ErrConflict
	}
	id := domain.NewID("ircd")
	realm := net.JoinHostPort("127.0.0.1", strconv.Itoa(ircPort))
	record := configRecord{Format: 1, IRCPort: ircPort, APIPort: apiPort, ServerName: "km" + strings.TrimPrefix(string(id), "ircd_") + ".local", Stage: "initializing"}
	c := Config{ID: id, Endpoint: "ircs://" + realm, RealmID: realm, APIEndpoint: "https://127.0.0.1:" + strconv.Itoa(apiPort), CAFile: filepath.Join(m.dir, "ca.pem"), CredentialRef: filepath.Join(m.dir, "bot.json"), Account: "keelmux-bot", Runtime: runtime}
	record.Config = c
	ca, cert, key, e := certificates(record.ServerName)
	if e != nil {
		return Config{}, e
	}
	token, e := randomHex(32)
	if e != nil {
		return Config{}, e
	}
	password, e := randomHex(32)
	if e != nil {
		return Config{}, e
	}
	secret, _ := json.Marshal(saslSecret{Account: c.Account, Password: password})
	for name, data := range map[string][]byte{"ca.pem": ca, "server.pem": cert, "server.key": key, "admin.token": []byte(token), "bot.json": secret} {
		if publish(filepath.Join(m.dir, name), data, 0600, false) != nil {
			return Config{}, ErrRepair
		}
	}
	template, e := readFile(filepath.Join(m.dir, "runtime", "default.yaml"), 128<<10, 0600)
	if e != nil {
		return Config{}, ErrRepair
	}
	if publish(filepath.Join(m.dir, "ergo.log"), nil, 0600, false) != nil {
		return Config{}, ErrRepair
	}
	yaml, e := m.configuration(string(template), record, token)
	if e != nil {
		return Config{}, e
	}
	if publish(filepath.Join(m.dir, "ergo.yaml"), []byte(yaml), 0600, false) != nil {
		return Config{}, ErrRepair
	}
	record.YAMLSHA256 = digest([]byte(yaml))
	record.CASHA256 = digest(ca)
	record.CertSHA256 = digest(cert)
	record.KeySHA256 = digest(key)
	if writeJSON(filepath.Join(m.dir, "config.json"), record, false) != nil {
		return Config{}, ErrRepair
	}
	if _, e = m.command(ctx, runtime.Path, "initdb", "--conf", filepath.Join(m.dir, "ergo.yaml"), "--quiet"); e != nil {
		return Config{}, ErrUncertain
	}
	if _, e = readFile(filepath.Join(m.dir, "ergo.db"), 64<<20, 0600); e != nil {
		return Config{}, ErrRepair
	}
	record.Stage = "ready"
	if writeJSON(filepath.Join(m.dir, "config.json"), record, true) != nil {
		return Config{}, ErrRepair
	}
	return c, nil
}
func loopbackPort(port int) (int, error) {
	if port < 0 || port > 65535 {
		return 0, ErrConflict
	}
	l, e := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if e != nil {
		return 0, ErrConflict
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
func quote(s string) string { b, _ := json.Marshal(s); return string(b) }
func (m *Manager) configuration(template string, r configRecord, token string) (string, error) {
	replacements := []struct{ path, value string }{
		{"network.name", quote("Keelmux")}, {"server.name", quote(r.ServerName)},
		{"server.listeners", quote(r.Config.RealmID) + ":\n    tls:\n        cert: " + quote(filepath.Join(m.dir, "server.pem")) + "\n        key: " + quote(filepath.Join(m.dir, "server.key")) + "\n    min-tls-version: '1.2'"},
		{"server.tor-listeners", "{}"}, {"server.websockets", "enabled: false"}, {"server.proxy-allowed-from", "[]"}, {"server.webirc", "[]"}, {"server.motd", quote("")}, {"server.relaymsg.enabled", "false"},
		{"accounts.registration.enabled", "false"}, {"accounts.registration.allow-before-connect", "false"}, {"accounts.authentication-enabled", "true"}, {"accounts.require-sasl.enabled", "true"}, {"accounts.require-sasl.exempted", "[]"}, {"accounts.login-via-pass-command", "false"}, {"accounts.auth-script.enabled", "false"}, {"accounts.oauth2.enabled", "false"}, {"accounts.jwt-auth.enabled", "false"}, {"accounts.multiclient.always-on", quote("disabled")},
		{"channels.registration.enabled", "true"}, {"channels.registration.operator-only", "false"}, {"channels.registration.max-channels-per-account", "128"},
		{"opers", "{}"}, {"logging", "- method: file\n  filename: " + quote(filepath.Join(m.dir, "ergo.log")) + "\n  type: '* -userinput -useroutput -connect-ip'\n  level: warn"},
		{"debug", "recover-from-errors: true\npprof-listener: ''"}, {"lock-file", quote(filepath.Join(m.dir, "ergo.lock"))}, {"datastore.path", quote(filepath.Join(m.dir, "ergo.db"))}, {"datastore.autoupgrade", "false"}, {"languages.enabled", "false"}, {"allow-environment-overrides", "false"}, {"metadata.enabled", "false"}, {"webpush.enabled", "false"},
		{"api", "enabled: true\nlistener: " + quote("127.0.0.1:"+strconv.Itoa(r.APIPort)) + "\ntls:\n    cert: " + quote(filepath.Join(m.dir, "server.pem")) + "\n    key: " + quote(filepath.Join(m.dir, "server.key")) + "\nbearer-tokens:\n    - " + quote(token)},
	}
	var e error
	for _, replace := range replacements {
		template, e = replaceYAML(template, strings.Split(replace.path, "."), replace.value)
		if e != nil {
			return "", e
		}
	}
	return template, nil
}

// The input is the checksum-verified upstream version's exact default YAML.
// Refuse missing/duplicate paths instead of accepting a changed config grammar.
func replaceYAML(text string, path []string, value string) (string, error) {
	lines := strings.Split(text, "\n")
	begin, end, indent := 0, len(lines), 0
	found := -1
	for _, key := range path {
		found = -1
		for i := begin; i < end; i++ {
			if strings.HasPrefix(lines[i], strings.Repeat(" ", indent)+key+":") {
				if found != -1 {
					return "", ErrRepair
				}
				found = i
			}
		}
		if found < 0 {
			return "", ErrRepair
		}
		next := end
		for i := found + 1; i < end; i++ {
			trim := strings.TrimSpace(lines[i])
			if trim == "" || strings.HasPrefix(trim, "#") {
				continue
			}
			spaces := len(lines[i]) - len(strings.TrimLeft(lines[i], " "))
			if spaces <= indent {
				next = i
				break
			}
		}
		begin, end = found+1, next
		indent += 4
	}
	indent -= 4
	prefix := strings.Repeat(" ", indent) + path[len(path)-1] + ":"
	replacement := []string{}
	if strings.Contains(value, "\n") || (len(path) == 2 && path[0] == "server" && path[1] == "websockets") {
		replacement = append(replacement, prefix)
		for _, line := range strings.Split(value, "\n") {
			replacement = append(replacement, strings.Repeat(" ", indent+4)+line)
		}
	} else {
		replacement = []string{prefix + " " + value}
	}
	out := append([]string{}, lines[:found]...)
	out = append(out, replacement...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n"), nil
}
func certificates(name string) ([]byte, []byte, []byte, error) {
	_, caKey, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return nil, nil, nil, ErrUnavailable
	}
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return nil, nil, nil, ErrUnavailable
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return nil, nil, nil, ErrUnavailable
	}
	now := time.Now().UTC()
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name + " private CA"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, e := x509.CreateCertificate(rand.Reader, ca, ca, caKey.Public(), caKey)
	if e != nil {
		return nil, nil, nil, ErrUnavailable
	}
	leaf := &x509.Certificate{SerialNumber: new(big.Int).Add(serial, big.NewInt(1)), Subject: pkix.Name{CommonName: name}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, DNSNames: []string{name}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, leaf, ca, key.Public(), caKey)
	if e != nil {
		return nil, nil, nil, ErrUnavailable
	}
	priv, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		return nil, nil, nil, ErrUnavailable
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), nil
}
func (m *Manager) readConfig(ctx context.Context) (configRecord, error) {
	var r configRecord
	e := readJSON(filepath.Join(m.dir, "config.json"), &r)
	if e != nil {
		return r, e
	}
	c := r.Config
	if r.Format != 1 || (r.Stage != "ready" && r.Stage != "initializing") || !validLocalID(c.ID, "ircd") || c.CAFile != filepath.Join(m.dir, "ca.pem") || c.CredentialRef != filepath.Join(m.dir, "bot.json") || c.Account != "keelmux-bot" || r.IRCPort < 1 || r.IRCPort > 65535 || r.APIPort < 1 || r.APIPort > 65535 || r.IRCPort == r.APIPort {
		return r, ErrRepair
	}
	realm := net.JoinHostPort("127.0.0.1", strconv.Itoa(r.IRCPort))
	if c.RealmID != realm || c.Endpoint != "ircs://"+realm || c.APIEndpoint != "https://127.0.0.1:"+strconv.Itoa(r.APIPort) || r.ServerName != "km"+strings.TrimPrefix(string(c.ID), "ircd_")+".local" {
		return r, ErrRepair
	}
	runtime, e := m.runtimeInfo(ctx)
	if e != nil || runtime != c.Runtime {
		return r, ErrRepair
	}
	for name, want := range map[string]string{"ergo.yaml": r.YAMLSHA256, "ca.pem": r.CASHA256, "server.pem": r.CertSHA256, "server.key": r.KeySHA256} {
		b, e := readFile(filepath.Join(m.dir, name), 128<<10, 0600)
		if e != nil || digest(b) != want || len(want) != 64 {
			return r, ErrRepair
		}
	}
	return r, nil
}
func (m *Manager) Config() (Config, error) {
	r, e := m.readConfig(context.Background())
	if os.IsNotExist(e) {
		return Config{}, ErrNotInitialized
	}
	if e != nil {
		return Config{}, e
	}
	return r.Config, nil
}
func (m *Manager) ScopedTLS() (*tls.Config, error) {
	c, e := m.Config()
	if e != nil {
		return nil, e
	}
	ca, e := readFile(c.CAFile, 8192, 0600)
	if e != nil {
		return nil, ErrRepair
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, ErrRepair
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: "127.0.0.1"}, nil
}
func (m *Manager) Dial(ctx context.Context, authority string) (net.Conn, error) {
	c, e := m.Config()
	if e != nil || authority != c.RealmID {
		return nil, ErrConflict
	}
	conf, e := m.ScopedTLS()
	if e != nil {
		return nil, e
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: conf}
	conn, e := d.DialContext(ctx, "tcp", authority)
	if e != nil {
		return nil, ErrUnavailable
	}
	return conn, nil
}
