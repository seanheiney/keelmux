package library

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const ManifestFilename = "keelmux-plugin.json"
const manifestLimit = 256 << 10
const fileLimit = 1 << 20
const totalLimit = 16 << 20
const maxFiles = 512

var ErrInvalid = errors.New("invalid capability manifest or selection")
var ErrCorrupt = errors.New("capability library is corrupt; preserve it for explicit repair")
var ErrUnsafe = errors.New("capability library path must be owned and safe")
var ErrNotFound = errors.New("capability package or profile not found")
var ErrPending = errors.New("capability library has an unfinished write; retry a mutation to recover")
var ErrStale = errors.New("capability selection changed; refresh and retry")

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func validID(s string) bool { return identifier.MatchString(s) }
func validText(s string, limit int) bool {
	if len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func relative(s string) bool {
	return s != "" && len(s) <= 512 && validText(s, 512) && !strings.Contains(s, "\\") && !path.IsAbs(s) && path.Clean(s) == s && s != "." && s != ".." && !strings.HasPrefix(s, "../") && len(strings.Split(s, "/")) <= 16
}

// strictJSON rejects duplicate keys, case aliases, unknown fields and excessive
// nesting before binding data to a schema. Map keys remain case-sensitive data.
func strictJSON(raw []byte, out any, limit int) error {
	if len(raw) > limit || !utf8.Valid(raw) {
		return ErrCorrupt
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := distinct(d, 0); err != nil {
		return ErrCorrupt
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrCorrupt
	}
	var generic any
	d = json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&generic); err != nil {
		return ErrCorrupt
	}
	if err := exactFields(generic, reflect.TypeOf(out).Elem()); err != nil {
		return ErrCorrupt
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return ErrCorrupt
	}
	return nil
}
func distinct(d *json.Decoder, depth int) error {
	if depth > 24 {
		return ErrCorrupt
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return ErrCorrupt
			}
			seen[s] = true
		}
		if err := distinct(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}
func exactFields(value any, t reflect.Type) error {
	if value == nil {
		return nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		return exactFields(value, t.Elem())
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return ErrCorrupt
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if tag != "" && tag != "-" {
				fields[tag] = f.Type
			}
		}
		for k, v := range object {
			ft, ok := fields[k]
			if !ok {
				return ErrCorrupt
			}
			if err := exactFields(v, ft); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return ErrCorrupt
		}
		for _, v := range object {
			if err := exactFields(v, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice:
		values, ok := value.([]any)
		if !ok {
			return ErrCorrupt
		}
		for _, v := range values {
			if err := exactFields(v, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateManifest(m Manifest) error {
	if m.Schema != SchemaVersion || !validID(m.ID) || m.Version == "" || !validText(m.Version, 128) || !validText(m.Description, 1024) || !validText(m.Source, 1024) || !validText(m.Revision, 256) || m.Files == nil || len(m.Files) > maxFiles || len(m.Skills)+len(m.Rules)+len(m.Tools) > 512 {
		return ErrInvalid
	}
	for name, hash := range m.Files {
		if !relative(name) || name == ManifestFilename || !digestPattern.MatchString(hash) {
			return ErrInvalid
		}
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			if _, ok := m.Files[dir]; ok {
				return ErrInvalid
			}
		}
	}
	for kind, assets := range map[string][]Asset{"skill": m.Skills, "rule": m.Rules} {
		seen := map[string]bool{}
		for _, a := range assets {
			if !validID(a.ID) || seen[a.ID] || !relative(a.Path) || !validText(a.Description, 1024) {
				return ErrInvalid
			}
			seen[a.ID] = true
			if kind == "rule" {
				if _, ok := m.Files[a.Path]; !ok || path.Ext(a.Path) != ".md" {
					return ErrInvalid
				}
			} else {
				if _, ok := m.Files[a.Path+"/SKILL.md"]; !ok {
					return ErrInvalid
				}
			}
		}
	}
	seen := map[string]bool{}
	for _, t := range m.Tools {
		if !validID(t.ID) || seen[t.ID] || !validText(t.Description, 1024) || len(t.EnvRefs) > 64 {
			return ErrInvalid
		}
		seen[t.ID] = true
		refs := map[string]bool{}
		for _, e := range t.EnvRefs {
			if !envName.MatchString(e) || refs[e] {
				return ErrInvalid
			}
			refs[e] = true
		}
		switch t.Transport {
		case "stdio":
			if t.URL != "" || len(t.Command) == 0 || len(t.Command) > 64 {
				return ErrInvalid
			}
			for _, a := range t.Command {
				if a == "" || !validText(a, 4096) {
					return ErrInvalid
				}
			}
		case "https":
			u, err := url.Parse(t.URL)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(t.Command) != 0 || !validText(t.URL, 4096) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	return nil
}
func normalizeManifest(m Manifest) Manifest {
	sort.Slice(m.Skills, func(i, j int) bool { return m.Skills[i].ID < m.Skills[j].ID })
	sort.Slice(m.Rules, func(i, j int) bool { return m.Rules[i].ID < m.Rules[j].ID })
	sort.Slice(m.Tools, func(i, j int) bool { return m.Tools[i].ID < m.Tools[j].ID })
	for i := range m.Tools {
		sort.Strings(m.Tools[i].EnvRefs)
	}
	return m
}
func hashBytes(raw []byte) string  { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func packDigest(m Manifest) string { raw, _ := json.Marshal(m); return hashBytes(raw) }
func componentExists(m Manifest, key string) bool {
	parts := strings.Split(key, "/")
	if len(parts) != 2 || !validID(parts[1]) {
		return false
	}
	switch parts[0] {
	case "skill":
		for _, a := range m.Skills {
			if a.ID == parts[1] {
				return true
			}
		}
	case "rule":
		for _, a := range m.Rules {
			if a.ID == parts[1] {
				return true
			}
		}
	case "tool":
		for _, a := range m.Tools {
			if a.ID == parts[1] {
				return true
			}
		}
	}
	return false
}
