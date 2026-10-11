package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
	"unicode/utf8"
)

// ReadPrivateJSON decodes one bounded owner-owned mode-0600 configuration.
// Configuration schemas use exact lower snake case tags. Refusing aliases and
// duplicate members prevents JSON's last-key-wins behavior from hiding scope.
func ReadPrivateJSON(path string, limit int64, out any) error {
	invalid := errors.New("private configuration must be owned mode-0600 bounded unambiguous JSON")
	if limit <= 0 || limit > 1<<20 {
		return invalid
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return invalid
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || info.Size() > limit {
		return invalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	current, identityErr := os.Lstat(path)
	if err != nil || identityErr != nil || !os.SameFile(info, current) || current.Mode().Perm() != 0600 || int64(len(raw)) > limit || !utf8.Valid(raw) {
		return invalid
	}
	check := json.NewDecoder(bytes.NewReader(raw))
	check.UseNumber()
	if distinctConfigKeys(check, 0) != nil {
		return invalid
	}
	if !errors.Is(check.Decode(new(any)), io.EOF) {
		return invalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		return invalid
	}
	return nil
}

func distinctConfigKeys(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("configuration depth exceeded")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return errors.New("invalid configuration")
	}
	seen := map[string]bool{}
	for d.More() {
		if delimiter == '{' {
			token, err = d.Token()
			key, ok := token.(string)
			if err != nil || !ok || key == "" || seen[key] {
				return errors.New("ambiguous configuration field")
			}
			for _, r := range key {
				if r != '_' && (r < 'a' || r > 'z') {
					return errors.New("configuration fields require exact lower snake case")
				}
			}
			seen[key] = true
		}
		if err = distinctConfigKeys(d, depth+1); err != nil {
			return err
		}
	}
	token, err = d.Token()
	if err != nil || delimiter == '{' && token != json.Delim('}') || delimiter == '[' && token != json.Delim(']') {
		return errors.New("invalid configuration ending")
	}
	return nil
}
