package codexbroker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

const (
	policyMaxBytes = 1 << 20
	policyMaxDepth = 32
	policyMaxNodes = 65536
)

var (
	errPolicy     = errors.New("Codex broker policy refused the frame")
	errProjection = errors.New("Codex broker does not support this native projection")
)

// readJSON retains integer precision and rejects duplicate decoded keys at every
// depth. encoding/json alone normalizes invalid Unicode, which is unsafe for IDs.
func readJSON(raw []byte) (any, error) {
	if len(raw) == 0 || len(raw) > policyMaxBytes || !utf8.Valid(raw) || !validEscapes(raw) {
		return nil, errPolicy
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	nodes := 0
	v, err := readValue(d, 0, &nodes)
	if err != nil {
		return nil, errPolicy
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errPolicy
	}
	return v, nil
}

func readValue(d *json.Decoder, depth int, nodes *int) (any, error) {
	*nodes++
	if depth > policyMaxDepth || *nodes > policyMaxNodes {
		return nil, errPolicy
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			m := map[string]any{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return nil, err
				}
				key, ok := k.(string)
				if !ok || len(key) > 4096 {
					return nil, errPolicy
				}
				if _, exists := m[key]; exists {
					return nil, errPolicy
				}
				v, err := readValue(d, depth+1, nodes)
				if err != nil {
					return nil, err
				}
				m[key] = v
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errPolicy
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, err := readValue(d, depth+1, nodes)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errPolicy
			}
			return a, nil
		}
		return nil, errPolicy
	case string:
		if len(x) > 256*1024 {
			return nil, errPolicy
		}
	}
	return t, nil
}

func validEscapes(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inString = !inString
		} else if inString && raw[i] == '\\' {
			i++
			if i >= len(raw) {
				return false
			}
			if raw[i] != 'u' {
				continue
			}
			if i+4 >= len(raw) {
				return false
			}
			v, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
			if err != nil {
				return false
			}
			i += 4
			if v >= 0xdc00 && v <= 0xdfff {
				return false
			}
			if v >= 0xd800 && v <= 0xdbff {
				if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return false
				}
				low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			}
		}
	}
	return !inString
}

func canonicalID(v any) (json.RawMessage, bool) {
	switch x := v.(type) {
	case string:
		if len(x) == 0 || len(x) > 256 {
			return nil, false
		}
		b, err := json.Marshal(x)
		return b, err == nil
	case json.Number:
		i, err := strconv.ParseInt(string(x), 10, 64)
		if err == nil {
			return []byte(strconv.FormatInt(i, 10)), true
		}
	}
	return nil, false
}

func validatedID(raw []byte) (json.RawMessage, bool) {
	v, err := readJSON(raw)
	if err != nil {
		return nil, false
	}
	return canonicalID(v)
}

func (*nativePolicy) Decode(raw []byte) (Frame, error) {
	v, err := readJSON(raw)
	if err != nil {
		return Frame{}, err
	}
	m, ok := v.(map[string]any)
	if !ok || !onlyKeys(m, "jsonrpc", "id", "method", "params", "result", "error") {
		return Frame{}, errPolicy
	}
	if version, present := m["jsonrpc"]; present && version != "2.0" {
		return Frame{}, errPolicy
	}
	var f Frame
	if id, present := m["id"]; present {
		var valid bool
		f.ID, valid = canonicalID(id)
		if !valid {
			return Frame{}, errPolicy
		}
	}
	if method, present := m["method"]; present {
		s, valid := method.(string)
		if !valid || len(s) == 0 || len(s) > 128 || mHas(m, "result") || mHas(m, "error") {
			return Frame{}, errPolicy
		}
		f.Method = s
		f.Kind = Notification
		if f.ID != nil {
			f.Kind = Request
		}
		if params, present := m["params"]; present {
			if _, valid := params.(map[string]any); !valid {
				return Frame{}, errPolicy
			}
			f.Payload, _ = json.Marshal(params)
		}
		return f, nil
	}
	if f.ID == nil || mHas(m, "params") || mHas(m, "result") == mHas(m, "error") {
		return Frame{}, errPolicy
	}
	if result, present := m["result"]; present {
		f.Kind = Result
		f.Payload, _ = json.Marshal(result)
	} else {
		e, valid := m["error"].(map[string]any)
		if !valid || !onlyKeys(e, "code", "message", "data") || !integer(e["code"], -1<<63, 1<<63-1) || !stringValue(e["message"], 8192, true) {
			return Frame{}, errPolicy
		}
		f.Kind = RPCError
		f.Payload, _ = json.Marshal(e)
	}
	return f, nil
}

func mHas(m map[string]any, key string) bool { _, ok := m[key]; return ok }
func onlyKeys(m map[string]any, keys ...string) bool {
	for key := range m {
		found := false
		for _, allowed := range keys {
			found = found || key == allowed
		}
		if !found {
			return false
		}
	}
	return true
}
func stringValue(v any, max int, empty bool) bool {
	s, ok := v.(string)
	return ok && len(s) <= max && (empty || len(s) > 0)
}
func integer(v any, min, max int64) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	return err == nil && i >= min && i <= max
}
func nullOnly(m map[string]any, keys ...string) bool {
	for _, key := range keys {
		if m[key] != nil {
			return false
		}
	}
	return true
}
func boolOrNull(v any) bool {
	if v == nil {
		return true
	}
	_, ok := v.(bool)
	return ok
}
func enumOrNull(v any, choices ...string) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, choice := range choices {
		if s == choice {
			return true
		}
	}
	return false
}
func stringsArray(v any, max int, choices ...string) bool {
	a, ok := v.([]any)
	if !ok || len(a) > max {
		return false
	}
	for _, x := range a {
		if !stringValue(x, 4096, false) || (len(choices) > 0 && !enumOrNull(x, choices...)) {
			return false
		}
	}
	return true
}
func payloadObject(f Frame, absent bool) (map[string]any, bool) {
	if len(f.Payload) == 0 {
		return map[string]any{}, absent
	}
	v, err := readJSON(f.Payload)
	m, ok := v.(map[string]any)
	return m, err == nil && ok
}
