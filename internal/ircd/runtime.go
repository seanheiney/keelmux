package ircd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"github.com/seanheiney/keelmux/internal/config"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type pin struct{ asset, sha string }

var releasePins = map[string]pin{
	"darwin/arm64": {"ergo-2.19.1-macos-arm64.tar.gz", "1bd97a0917036061e2dcdfc29149c2e252e3bc856282e0956afa2aaaa54dd787"},
	"darwin/amd64": {"ergo-2.19.1-macos-x86_64.tar.gz", "68b388a49034257082ee0292703874630a2c10cfba86c73bb2adc37b515c57a3"},
	"linux/arm64":  {"ergo-2.19.1-linux-arm64.tar.gz", "3f68fee0ede09eeeb205a553fe9727b409c686752e1cc2fea34ae2f9d64517fb"},
	"linux/amd64":  {"ergo-2.19.1-linux-x86_64.tar.gz", "c275f2de8e8eb074c38707136392290172400b5b78616cb9f660351527924b47"},
}

func nativePin() (string, pin, error) {
	target := runtime.GOOS + "/" + runtime.GOARCH
	p, ok := releasePins[target]
	if !ok {
		return "", pin{}, ErrUnavailable
	}
	return target, p, nil
}
func (m *Manager) Download(ctx context.Context) (RuntimeInfo, error) {
	unlock, e := m.lock(ctx, true)
	if e != nil {
		return RuntimeInfo{}, e
	}
	defer unlock()
	return m.download(ctx)
}
func (m *Manager) download(ctx context.Context) (RuntimeInfo, error) {
	if r, e := m.runtimeInfo(ctx); e == nil {
		return r, nil
	} else if !os.IsNotExist(e) {
		return RuntimeInfo{}, e
	}
	_, p, e := nativePin()
	if e != nil {
		return RuntimeInfo{}, e
	}
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" || req.URL.User != nil || (req.URL.Port() != "" && req.URL.Port() != "443") {
			return ErrUnavailable
		}
		switch req.URL.Hostname() {
		case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
			return nil
		}
		return ErrUnavailable
	}}
	req, e := http.NewRequestWithContext(ctx, "GET", "https://github.com/ergochat/ergo/releases/download/v2.19.1/"+p.asset, nil)
	if e != nil {
		return RuntimeInfo{}, ErrUnavailable
	}
	response, e := client.Do(req)
	if e != nil {
		return RuntimeInfo{}, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return RuntimeInfo{}, ErrUnavailable
	}
	return m.installArchive(ctx, response.Body)
}

// InstallArchive supports explicit offline provisioning. The native archive pin
// is still mandatory; alternate versions, mirrors and targets cannot be adopted.
func (m *Manager) InstallArchive(ctx context.Context, archive io.Reader) (RuntimeInfo, error) {
	unlock, e := m.lock(ctx, true)
	if e != nil {
		return RuntimeInfo{}, e
	}
	defer unlock()
	return m.installArchive(ctx, archive)
}
func (m *Manager) installArchive(ctx context.Context, archive io.Reader) (RuntimeInfo, error) {
	if r, e := m.runtimeInfo(ctx); e == nil {
		return r, nil
	} else if !os.IsNotExist(e) {
		return RuntimeInfo{}, e
	}
	target, p, e := nativePin()
	if e != nil {
		return RuntimeInfo{}, e
	}
	raw, e := io.ReadAll(io.LimitReader(archive, (32<<20)+1))
	if e != nil || len(raw) > 32<<20 || digest(raw) != p.sha {
		return RuntimeInfo{}, ErrUnavailable
	}
	files, e := unpack(raw, strings.TrimSuffix(p.asset, ".tar.gz"))
	if e != nil {
		return RuntimeInfo{}, e
	}
	dir := filepath.Join(m.dir, "runtime")
	if os.Mkdir(dir, 0700) != nil {
		return RuntimeInfo{}, ErrRepair
	}
	if syncDir(m.dir) != nil {
		return RuntimeInfo{}, ErrRepair
	}
	for _, name := range []string{"ergo", "LICENSE", "default.yaml"} {
		mode := os.FileMode(0600)
		if name == "ergo" {
			mode = 0700
		}
		if publish(filepath.Join(dir, name), files[name], mode, false) != nil {
			return RuntimeInfo{}, ErrRepair
		}
	}
	r := RuntimeInfo{Version: Version, Source: SourceCommit, Target: target, ArchiveSHA256: p.sha, BinarySHA256: digest(files["ergo"]), DefaultSHA256: digest(files["default.yaml"]), LicenseSHA256: digest(files["LICENSE"]), Path: filepath.Join(dir, "ergo")}
	out, e := m.command(ctx, r.Path, "--version")
	if e != nil || strings.TrimSpace(string(out)) != "ergo-"+Version {
		return RuntimeInfo{}, ErrUnavailable
	}
	if writeJSON(filepath.Join(m.dir, "runtime.json"), r, false) != nil {
		return RuntimeInfo{}, ErrRepair
	}
	return r, nil
}
func unpack(raw []byte, prefix string) (map[string][]byte, error) {
	compressed := bytes.NewReader(raw)
	gz, e := gzip.NewReader(compressed)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer gz.Close()
	gz.Multistream(false)
	expanded, e := io.ReadAll(io.LimitReader(gz, (64<<20)+1))
	if e != nil || len(expanded) > 64<<20 || compressed.Len() != 0 {
		return nil, ErrUnavailable
	}
	tarBytes := bytes.NewReader(expanded)
	tr := tar.NewReader(tarBytes)
	seen := map[string]bool{}
	result := map[string][]byte{}
	total := int64(0)
	for count := 0; ; count++ {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil || count >= 1024 {
			return nil, ErrUnavailable
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") || !(strings.HasPrefix(name, prefix+"/") || (name == prefix && h.Typeflag == tar.TypeDir)) || seen[name] || h.Size < 0 || h.Size > 32<<20 {
			return nil, ErrUnavailable
		}
		seen[name] = true
		if h.Typeflag == tar.TypeDir {
			if h.Size != 0 {
				return nil, ErrUnavailable
			}
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return nil, ErrUnavailable
		}
		total += h.Size
		if total > 64<<20 {
			return nil, ErrUnavailable
		}
		short := strings.TrimPrefix(name, prefix+"/")
		if short == "ergo" || short == "LICENSE" || short == "default.yaml" {
			b, e := io.ReadAll(io.LimitReader(tr, h.Size+1))
			if e != nil || int64(len(b)) != h.Size {
				return nil, ErrUnavailable
			}
			result[short] = b
		} else if _, e := io.Copy(io.Discard, tr); e != nil {
			return nil, ErrUnavailable
		}
	}
	if len(result) != 3 || len(result["ergo"]) == 0 || len(result["default.yaml"]) == 0 || len(result["LICENSE"]) == 0 {
		return nil, ErrUnavailable
	}
	// Tar permits zero padding after its end marker; nonzero payload or a
	// second archive is rejected even though the outer archive pin is mandatory.
	tail, e := io.ReadAll(tarBytes)
	if e != nil {
		return nil, ErrUnavailable
	}
	for _, b := range tail {
		if b != 0 {
			return nil, ErrUnavailable
		}
	}
	return result, nil
}
func (m *Manager) runtimeInfo(ctx context.Context) (RuntimeInfo, error) {
	var r RuntimeInfo
	e := readJSON(filepath.Join(m.dir, "runtime.json"), &r)
	if e != nil {
		return r, e
	}
	if config.EnsurePrivateDirectory(filepath.Join(m.dir, "runtime")) != nil {
		return RuntimeInfo{}, ErrRepair
	}
	target, p, e := nativePin()
	if e != nil || r.Version != Version || r.Source != SourceCommit || r.Target != target || r.ArchiveSHA256 != p.sha || r.Path != filepath.Join(m.dir, "runtime", "ergo") {
		return r, ErrRepair
	}
	binary, e := readFile(r.Path, 32<<20, 0700)
	if e != nil || digest(binary) != r.BinarySHA256 || len(r.BinarySHA256) != 64 {
		return r, ErrRepair
	}
	for name, want := range map[string]string{"default.yaml": r.DefaultSHA256, "LICENSE": r.LicenseSHA256} {
		b, e := readFile(filepath.Join(m.dir, "runtime", name), 128<<10, 0600)
		if e != nil || digest(b) != want || len(want) != 64 {
			return r, ErrRepair
		}
	}
	return r, nil
}
