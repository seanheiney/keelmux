package primarystdio

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Codec is a stateless, pinned Codex 0.159.0 codec. It performs no I/O or
// readiness decisions. Envelopes and payloads stay private to the helper actor.
type Codec struct{}
type FrameKind uint8

const (
	Request FrameKind = iota + 1
	Notification
	Result
	RPCError
)

type Envelope struct {
	Kind        FrameKind
	ID          json.RawMessage
	Method      string
	Payload     json.RawMessage
	EmittedAtMS *int64
}
type PageCursorState uint8

const (
	PageCursorMissing PageCursorState = iota
	PageCursorNull
	PageCursorValue
)

type PageCursor struct {
	State PageCursorState
	Value string
}
type InitResult struct{ Home, PlatformFamily, PlatformOS string }
type ThreadStatus string

const (
	StatusNotLoaded   ThreadStatus = "notLoaded"
	StatusIdle        ThreadStatus = "idle"
	StatusSystemError ThreadStatus = "systemError"
	StatusActive      ThreadStatus = "active"
)

type ThreadResult struct {
	ID, CWD, ResumeCWD string
	Ephemeral          bool
	Status             ThreadStatus
	ActiveFlags        []string
}
type LoadedResult struct {
	IDs    []string
	Cursor PageCursor
}
type ListResult struct {
	Count  int
	Cursor PageCursor
}
type NoticeKind uint8

const (
	NoticeThreadStarted NoticeKind = iota + 1
	NoticeThreadStatus
	NoticeQueueChanged
	NoticeRemoteStatus
	NoticeUnsupported
)

type Notice struct {
	Kind             NoticeKind
	Method, ThreadID string
	Thread           *ThreadResult
	Status           ThreadStatus
	ActiveFlags      []string
	RemoteStatus     string
}

func validOutgoing(s string) bool {
	return len(s) > 0 && len(s) <= 256 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func encode(id, method string, params any) ([]byte, error) {
	if !validOutgoing(id) {
		return nil, ErrInvalid
	}
	raw, err := json.Marshal(struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{id, method, params})
	if err != nil || len(raw)+1 > MaxFrameBytes {
		return nil, ErrInvalid
	}
	return append(raw, '\n'), nil
}
func (Codec) Initialize(id string) ([]byte, error) {
	return encode(id, "initialize", map[string]any{"clientInfo": map[string]any{"name": "keelmux_owned_stdio", "title": "Keelmux owned stdio", "version": "0.1.0"}, "capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false, "explicitGatewayOauth": true, "optOutNotificationMethods": []string{}}})
}
func (Codec) Initialized() ([]byte, error) { return []byte("{\"method\":\"initialized\"}\n"), nil }
func (Codec) Resume(id, threadID string) ([]byte, error) {
	if !validOutgoing(threadID) {
		return nil, ErrInvalid
	}
	return encode(id, "thread/resume", map[string]any{"threadId": threadID, "excludeTurns": true})
}
func (Codec) Loaded(id string) ([]byte, error) {
	return encode(id, "thread/loaded/list", map[string]any{"limit": 2})
}
func (Codec) Read(id, threadID string) ([]byte, error) {
	if !validOutgoing(threadID) {
		return nil, ErrInvalid
	}
	return encode(id, "thread/read", map[string]any{"threadId": threadID, "includeTurns": false})
}
func (Codec) Queue(id, threadID string) ([]byte, error) {
	if !validOutgoing(threadID) {
		return nil, ErrInvalid
	}
	return encode(id, "thread/queue/list", map[string]any{"threadId": threadID, "limit": 1})
}
func (Codec) Background(id, threadID string) ([]byte, error) {
	if !validOutgoing(threadID) {
		return nil, ErrInvalid
	}
	return encode(id, "thread/backgroundTerminals/list", map[string]any{"threadId": threadID, "limit": 1})
}

// DecodeLine requires a complete JSONL frame; partial EOF is never a message.
// JSON-RPC's optional version is accepted, but not required by native Codex.
func (Codec) DecodeLine(line []byte) (Envelope, error) {
	var e Envelope
	if len(line) < 3 || len(line) > MaxFrameBytes || line[len(line)-1] != '\n' {
		return e, ErrInvalid
	}
	body := bytes.TrimSuffix(line[:len(line)-1], []byte("\r"))
	if bytes.ContainsAny(body, "\r\n") {
		return e, ErrInvalid
	}
	v, err := strictJSON(line)
	if err != nil {
		return e, err
	}
	m, ok := v.(map[string]any)
	if !ok || !keys(m, "jsonrpc", "id", "method", "params", "result", "error", "emittedAtMs", "trace") {
		return e, ErrInvalid
	}
	if x, has := m["jsonrpc"]; has && x != "2.0" {
		return e, ErrInvalid
	}
	if x, has := m["id"]; has {
		e.ID, ok = canonicalID(x)
		if !ok {
			return Envelope{}, ErrInvalid
		}
	}
	method, hasMethod := m["method"]
	_, hasResult := m["result"]
	_, hasError := m["error"]
	if hasMethod {
		s, ok := method.(string)
		if !ok || len(s) == 0 || len(s) > 128 || hasResult || hasError {
			return Envelope{}, ErrInvalid
		}
		e.Method = s
		e.Kind = Notification
		if e.ID != nil {
			e.Kind = Request
		}
		if x, has := m["params"]; has {
			if _, ok := x.(map[string]any); !ok {
				return Envelope{}, ErrInvalid
			}
			e.Payload, _ = json.Marshal(x)
		}
	} else {
		if e.ID == nil || hasResult == hasError || has(m, "params") {
			return Envelope{}, ErrInvalid
		}
		if hasResult {
			e.Kind = Result
			e.Payload, _ = json.Marshal(m["result"])
		} else {
			x, ok := m["error"].(map[string]any)
			if !ok || !keys(x, "code", "message", "data") || !int64Value(x["code"]) || !isString(x["message"]) {
				return Envelope{}, ErrInvalid
			}
			e.Kind = RPCError
			e.Payload, _ = json.Marshal(x)
		}
	}
	if stamp, present := m["emittedAtMs"]; present {
		n, ok := signed(stamp, 64)
		if e.Kind != Notification || !ok || n < 0 {
			return Envelope{}, ErrInvalid
		}
		e.EmittedAtMS = &n
	}
	if trace, present := m["trace"]; present {
		if e.Kind != Request {
			return Envelope{}, ErrInvalid
		}
		if trace != nil {
			t, ok := trace.(map[string]any)
			if !ok || !keys(t, "traceparent", "tracestate") {
				return Envelope{}, ErrInvalid
			}
			for _, x := range t {
				if x != nil && !isString(x) {
					return Envelope{}, ErrInvalid
				}
			}
		}
	}
	return e, nil
}
func canonicalID(v any) (json.RawMessage, bool) {
	if s, ok := v.(string); ok {
		if len(s) > 256 {
			return nil, false
		}
		b, _ := json.Marshal(s)
		return b, true
	}
	if n, ok := signed(v, 64); ok {
		return []byte(strconv.FormatInt(n, 10)), true
	}
	return nil, false
}
func has(m map[string]any, k string) bool { _, ok := m[k]; return ok }
func keys(m map[string]any, allowed ...string) bool {
	for k := range m {
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func isString(v any) bool { _, ok := v.(string); return ok }
func signed(v any, bits int) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	x, e := strconv.ParseInt(string(n), 10, bits)
	return x, e == nil
}
func int64Value(v any) bool { _, ok := signed(v, 64); return ok }

func result(e Envelope, id, root string) (map[string]any, error) {
	if !validOutgoing(id) {
		return nil, ErrInvalid
	}
	want, _ := json.Marshal(id)
	if !bytes.Equal(e.ID, want) {
		return nil, ErrConflict
	}
	if e.Kind == RPCError {
		return nil, ErrUnavailable
	}
	if e.Kind != Result || e.Method != "" || e.EmittedAtMS != nil {
		return nil, ErrInvalid
	}
	v, err := strictJSON(e.Payload)
	if err != nil {
		return nil, err
	}
	budget := 200000
	if !validate(codecSchemas.Roots[root], v, &budget, 0) {
		return nil, ErrInvalid
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ErrInvalid
	}
	return m, nil
}
func (Codec) ParseInitialize(e Envelope, id string) (InitResult, error) {
	m, err := result(e, id, "InitializeResponse")
	if err != nil {
		return InitResult{}, err
	}
	return InitResult{m["codexHome"].(string), m["platformFamily"].(string), m["platformOs"].(string)}, nil
}
func threadResult(m map[string]any) ThreadResult {
	status := m["status"].(map[string]any)
	r := ThreadResult{ID: m["id"].(string), CWD: m["cwd"].(string), Ephemeral: m["ephemeral"].(bool), Status: ThreadStatus(status["type"].(string))}
	if flags, ok := status["activeFlags"].([]any); ok {
		for _, f := range flags {
			r.ActiveFlags = append(r.ActiveFlags, f.(string))
		}
	}
	return r
}
func (Codec) ParseResume(e Envelope, id string) (ThreadResult, error) {
	m, err := result(e, id, "ThreadResumeResponse")
	if err != nil {
		return ThreadResult{}, err
	}
	r := threadResult(m["thread"].(map[string]any))
	r.ResumeCWD = m["cwd"].(string)
	return r, nil
}
func (Codec) ParseRead(e Envelope, id string) (ThreadResult, error) {
	m, err := result(e, id, "ThreadReadResponse")
	if err != nil {
		return ThreadResult{}, err
	}
	return threadResult(m["thread"].(map[string]any)), nil
}
func pageCursor(m map[string]any) PageCursor {
	x, present := m["nextCursor"]
	if !present {
		return PageCursor{State: PageCursorMissing}
	}
	if x == nil {
		return PageCursor{State: PageCursorNull}
	}
	return PageCursor{State: PageCursorValue, Value: x.(string)}
}
func (Codec) ParseLoaded(e Envelope, id string) (LoadedResult, error) {
	m, err := result(e, id, "ThreadLoadedListResponse")
	if err != nil {
		return LoadedResult{}, err
	}
	r := LoadedResult{Cursor: pageCursor(m)}
	for _, v := range m["data"].([]any) {
		r.IDs = append(r.IDs, v.(string))
	}
	return r, nil
}
func listResult(e Envelope, id, root string) (ListResult, error) {
	m, err := result(e, id, root)
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{Count: len(m["data"].([]any)), Cursor: pageCursor(m)}, nil
}
func (Codec) ParseQueue(e Envelope, id string) (ListResult, error) {
	return listResult(e, id, "ThreadQueueListResponse")
}
func (Codec) ParseBackground(e Envelope, id string) (ListResult, error) {
	return listResult(e, id, "ThreadBackgroundTerminalsListResponse")
}
func (Codec) ClassifyNotification(e Envelope) (Notice, error) {
	n := Notice{Kind: NoticeUnsupported, Method: e.Method}
	if e.Kind != Notification || e.ID != nil || e.Method == "" {
		return Notice{}, ErrInvalid
	}
	root := ""
	switch e.Method {
	case "thread/started":
		root = "ThreadStartedNotification"
	case "thread/status/changed":
		root = "ThreadStatusChangedNotification"
	case "thread/queue/changed":
		root = "ThreadQueueChangedNotification"
	case "remoteControl/status/changed":
		root = "RemoteControlStatusChangedNotification"
	default:
		return n, nil
	}
	v, err := strictJSON(e.Payload)
	if err != nil {
		return Notice{}, err
	}
	budget := 200000
	if !validate(codecSchemas.Roots[root], v, &budget, 0) {
		return Notice{}, ErrInvalid
	}
	m := v.(map[string]any)
	switch e.Method {
	case "thread/started":
		r := threadResult(m["thread"].(map[string]any))
		n.Kind = NoticeThreadStarted
		n.Thread = &r
		n.ThreadID = r.ID
	case "thread/status/changed":
		n.Kind = NoticeThreadStatus
		n.ThreadID = m["threadId"].(string)
		s := m["status"].(map[string]any)
		n.Status = ThreadStatus(s["type"].(string))
		if a, ok := s["activeFlags"].([]any); ok {
			for _, f := range a {
				n.ActiveFlags = append(n.ActiveFlags, f.(string))
			}
		}
	case "thread/queue/changed":
		n.Kind = NoticeQueueChanged
		n.ThreadID = m["threadId"].(string)
	case "remoteControl/status/changed":
		n.Kind = NoticeRemoteStatus
		n.RemoteStatus = m["status"].(string)
	}
	return n, nil
}

// strictJSON bounds nesting/node count and rejects duplicate decoded keys and
// unpaired surrogate escapes before encoding/json can normalize their identity.
func strictJSON(raw []byte) (any, error) {
	if len(raw) == 0 || len(raw) > MaxFrameBytes || !utf8.Valid(raw) || !validEscapes(raw) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	nodes := 0
	v, err := readValue(d, 0, &nodes)
	if err != nil {
		return nil, ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	return v, nil
}
func readValue(d *json.Decoder, depth int, nodes *int) (any, error) {
	*nodes++
	if depth > 32 || *nodes > 65536 {
		return nil, ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return nil, ErrInvalid
	}
	if x, ok := t.(json.Delim); ok {
		switch x {
		case '{':
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, ErrInvalid
				}
				s, ok := k.(string)
				if !ok || len(s) > 4096 || has(m, s) {
					return nil, ErrInvalid
				}
				v, e := readValue(d, depth+1, nodes)
				if e != nil {
					return nil, e
				}
				m[s] = v
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, ErrInvalid
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, e := readValue(d, depth+1, nodes)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, ErrInvalid
			}
			return a, nil
		}
		return nil, ErrInvalid
	}
	if s, ok := t.(string); ok && len(s) > 256<<10 {
		return nil, ErrInvalid
	}
	if n, ok := t.(json.Number); ok && len(n) > 64 {
		return nil, ErrInvalid
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

// schemaSet is generated from retained 0.159.0 schemas. Annotation fields are
// removed; validation keywords and named object properties are preserved.
type schemaSet struct {
	Roots       map[string]any `json:"roots"`
	Definitions map[string]any `json:"definitions"`
}

var codecSchemas = func() schemaSet {
	var s schemaSet
	d := json.NewDecoder(strings.NewReader(pinnedSchemaJSON))
	d.UseNumber()
	if d.Decode(&s) != nil {
		panic("invalid compiled primary schema")
	}
	return s
}()

// validate is private to the fixed response/notice parsers. Its work budget is
// shared across alternatives and references; input bounds alone are insufficient
// to bound adversarial union validation. Undeclared object fields are refused
// unless the pinned schema explicitly permits additional properties.
func validate(schema, v any, budget *int, depth int) bool {
	*budget--
	if *budget < 0 || depth > 96 {
		return false
	}
	if b, ok := schema.(bool); ok {
		return b
	}
	s, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	if ref, ok := s["$ref"].(string); ok {
		const prefix = "#/definitions/"
		if !strings.HasPrefix(ref, prefix) || !validate(codecSchemas.Definitions[strings.TrimPrefix(ref, prefix)], v, budget, depth+1) {
			return false
		}
	}
	for _, kind := range []string{"allOf", "anyOf", "oneOf"} {
		if a, ok := s[kind].([]any); ok {
			count := 0
			for _, part := range a {
				if validate(part, v, budget, depth+1) {
					count++
				}
				if *budget < 0 {
					return false
				}
			}
			if kind == "allOf" && count != len(a) || kind == "anyOf" && count == 0 || kind == "oneOf" && count != 1 {
				return false
			}
		}
	}
	if a, ok := s["enum"].([]any); ok {
		match := false
		for _, x := range a {
			if x == nil && v == nil {
				match = true
			}
			if x, ok := x.(string); ok {
				if y, ok := v.(string); ok && x == y {
					match = true
				}
			}
		}
		if !match {
			return false
		}
	}
	if t, ok := s["type"]; ok {
		match := false
		if name, ok := t.(string); ok {
			match = matchesType(name, v)
		}
		if names, ok := t.([]any); ok {
			for _, name := range names {
				if n, ok := name.(string); ok && matchesType(n, v) {
					match = true
				}
			}
		}
		if !match {
			return false
		}
	}
	if str, ok := v.(string); ok {
		if min, ok := s["minLength"].(json.Number); ok {
			n, err := strconv.Atoi(string(min))
			if err != nil || utf8.RuneCountInString(str) < n {
				return false
			}
		}
	}
	if n, ok := v.(json.Number); ok {
		if f, ok := s["format"].(string); ok {
			if !numberFormat(n, f) {
				return false
			}
		}
		if min, ok := s["minimum"].(json.Number); ok {
			a, e := strconv.ParseFloat(string(n), 64)
			b, f := strconv.ParseFloat(string(min), 64)
			if e != nil || f != nil || a < b {
				return false
			}
		}
	}
	if a, ok := v.([]any); ok {
		if item, exists := s["items"]; exists {
			for _, x := range a {
				if !validate(item, x, budget, depth+1) {
					return false
				}
			}
		}
	}
	if m, ok := v.(map[string]any); ok {
		if required, ok := s["required"].([]any); ok {
			for _, key := range required {
				k, ok := key.(string)
				if !ok || !has(m, k) {
					return false
				}
			}
		}
		props, hasProps := s["properties"].(map[string]any)
		extra, hasExtra := s["additionalProperties"]
		if hasProps || hasExtra {
			for key, x := range m {
				if sub, exists := props[key]; exists {
					if !validate(sub, x, budget, depth+1) {
						return false
					}
				} else if !hasExtra || !validate(extra, x, budget, depth+1) {
					return false
				}
			}
		}
	}
	return true
}
func matchesType(t string, v any) bool {
	switch t {
	case "null":
		return v == nil
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "string":
		return isString(v)
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "integer":
		n, ok := v.(json.Number)
		return ok && !strings.ContainsAny(string(n), ".eE")
	case "number":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		x, e := strconv.ParseFloat(string(n), 64)
		return e == nil && !math.IsInf(x, 0) && !math.IsNaN(x)
	}
	return false
}
func numberFormat(n json.Number, f string) bool {
	if f == "double" || f == "float" {
		x, err := strconv.ParseFloat(string(n), 64)
		return err == nil && !math.IsInf(x, 0) && !math.IsNaN(x)
	}
	bits := 64
	switch f {
	case "int32", "uint32":
		bits = 32
	case "uint16":
		bits = 16
	case "int64", "uint64", "uint":
	default:
		return false
	}
	if strings.HasPrefix(f, "uint") {
		_, e := strconv.ParseUint(string(n), 10, bits)
		return e == nil
	}
	_, e := strconv.ParseInt(string(n), 10, bits)
	return e == nil
}

// Retained generated schema inputs (0.159.0); pinned native binary SHA256
// e89718aa1969bfc4a471277bdc4679a3a3529293de0a309909822dfd67ddb77a.
// v1/InitializeResponse.json 62ad689c2cb6379913c1d72749cfd8de5089d35760214123518eb92eef11acc9
// v2/ThreadResumeResponse.json d841a3fc57d72033f21bad4816a1a1fbf52c912d23d0881f5de7fcf86c1f9a6e
// v2/ThreadReadResponse.json 2af6b987644cfb23e6f408f3cfb29e73ad6255ba2294366d36d56aeabe3854da
// v2/ThreadLoadedListResponse.json 1ea02c4710083e552b2a3a89b931b9049d5bf05b7db46278b8352cf9fd80cd52
// v2/ThreadQueueListResponse.json be4e0bfa6fc776101f96582543605d2eff6186309a08903203512b99c5005926
// v2/ThreadBackgroundTerminalsListResponse.json b56b1cbcb1d60079ee9f9caaeef29cad869a25b4a6d356566f7c9f672faa7bf4
// v2/ThreadStartedNotification.json f4c4acf11b4a71d72c19678583df1b5639561434b5156b12657951cda25cf0d1
// v2/ThreadStatusChangedNotification.json 26f3c60c1b73f7fa2d31c74429cdc36f8746c76c33e3d314b3fb61d3661f05f6
// v2/ThreadQueueChangedNotification.json 0a960da43fb14f69dae2550ac4f8517760464eedbf95a9977718548739fdcf21
// v2/RemoteControlStatusChangedNotification.json 56aa1d58294238557cd8b16199909c2c157a7542b9259ef2deddf40fe4c02c91
// Canonical compact schema SHA256 c3d1047ff5c3135141f4e6cfaab9c7b7bb6280938bb6e1365bc394344e0de819
const pinnedSchemaJSON = "{\"definitions\":{\"AbsolutePathBuf\":{\"type\":\"string\"},\"ActivePermissionProfile\":{\"properties\":{\"extends\":{\"type\":[\"string\",\"null\"]},\"id\":{\"type\":\"string\"}},\"required\":[\"id\"],\"type\":\"object\"},\"AgentMessageDelivery\":{\"enum\":[\"async\"],\"type\":\"string\"},\"AgentPath\":{\"type\":\"string\"},\"ApprovalsReviewer\":{\"enum\":[\"user\",\"auto_review\",\"guardian_subagent\"],\"type\":\"string\"},\"AskForApproval\":{\"oneOf\":[{\"enum\":[\"untrusted\",\"on-request\",\"never\"],\"type\":\"string\"},{\"additionalProperties\":false,\"properties\":{\"granular\":{\"properties\":{\"mcp_elicitations\":{\"type\":\"boolean\"},\"request_permissions\":{\"type\":\"boolean\"},\"rules\":{\"type\":\"boolean\"},\"sandbox_approval\":{\"type\":\"boolean\"},\"skill_approval\":{\"type\":\"boolean\"}},\"required\":[\"mcp_elicitations\",\"rules\",\"sandbox_approval\"],\"type\":\"object\"}},\"required\":[\"granular\"],\"type\":\"object\"}]},\"AsyncUserInputQuestion\":{\"additionalProperties\":false,\"properties\":{\"options\":{\"items\":{\"type\":\"string\"},\"type\":[\"array\",\"null\"]},\"title\":{\"type\":\"string\"}},\"required\":[\"title\"],\"type\":\"object\"},\"ByteRange\":{\"properties\":{\"end\":{\"format\":\"uint\",\"minimum\":0.0,\"type\":\"integer\"},\"start\":{\"format\":\"uint\",\"minimum\":0.0,\"type\":\"integer\"}},\"required\":[\"end\",\"start\"],\"type\":\"object\"},\"CodexErrorInfo\":{\"oneOf\":[{\"enum\":[\"contextWindowExceeded\",\"sessionBudgetExceeded\",\"usageLimitExceeded\",\"rateLimitExceeded\",\"flexUnavailable\",\"serverOverloaded\",\"cyberPolicy\",\"misalignmentPolicyViolation\",\"tooManyDenials\",\"internalServerError\",\"unauthorized\",\"badRequest\",\"threadRollbackFailed\",\"sandboxError\",\"other\"],\"type\":\"string\"},{\"additionalProperties\":false,\"properties\":{\"httpConnectionFailed\":{\"properties\":{\"httpStatusCode\":{\"format\":\"uint16\",\"minimum\":0.0,\"type\":[\"integer\",\"null\"]}},\"type\":\"object\"}},\"required\":[\"httpConnectionFailed\"],\"type\":\"object\"},{\"additionalProperties\":false,\"properties\":{\"responseStreamConnectionFailed\":{\"properties\":{\"httpStatusCode\":{\"format\":\"uint16\",\"minimum\":0.0,\"type\":[\"integer\",\"null\"]}},\"type\":\"object\"}},\"required\":[\"responseStreamConnectionFailed\"],\"type\":\"object\"},{\"additionalProperties\":false,\"properties\":{\"responseStreamDisconnected\":{\"properties\":{\"httpStatusCode\":{\"format\":\"uint16\",\"minimum\":0.0,\"type\":[\"integer\",\"null\"]}},\"type\":\"object\"}},\"required\":[\"responseStreamDisconnected\"],\"type\":\"object\"},{\"additionalProperties\":false,\"properties\":{\"responseTooManyFailedAttempts\":{\"properties\":{\"httpStatusCode\":{\"format\":\"uint16\",\"minimum\":0.0,\"type\":[\"integer\",\"null\"]}},\"type\":\"object\"}},\"required\":[\"responseTooManyFailedAttempts\"],\"type\":\"object\"},{\"additionalProperties\":false,\"properties\":{\"activeTurnNotSteerable\":{\"properties\":{\"turnKind\":{\"$ref\":\"#/definitions/NonSteerableTurnKind\"}},\"required\":[\"turnKind\"],\"type\":\"object\"}},\"required\":[\"activeTurnNotSteerable\"],\"type\":\"object\"}]},\"CollabAgentState\":{\"properties\":{\"message\":{\"type\":[\"string\",\"null\"]},\"status\":{\"$ref\":\"#/definitions/CollabAgentStatus\"}},\"required\":[\"status\"],\"type\":\"object\"},\"CollabAgentStatus\":{\"enum\":[\"pendingInit\",\"running\",\"interrupted\",\"completed\",\"errored\",\"shutdown\",\"notFound\"],\"type\":\"string\"},\"CollabAgentTool\":{\"enum\":[\"spawnAgent\",\"sendInput\",\"resumeAgent\",\"wait\",\"closeAgent\",\"sendMessage\",\"followupTask\",\"interruptAgent\",\"listAgents\"],\"type\":\"string\"},\"CollabAgentToolCallStatus\":{\"enum\":[\"inProgress\",\"completed\",\"failed\",\"interrupted\"],\"type\":\"string\"},\"CollaborationMode\":{\"properties\":{\"mode\":{\"$ref\":\"#/definitions/ModeKind\"},\"settings\":{\"$ref\":\"#/definitions/Settings\"}},\"required\":[\"mode\",\"settings\"],\"type\":\"object\"},\"CommandAction\":{\"oneOf\":[{\"properties\":{\"command\":{\"type\":\"string\"},\"name\":{\"type\":\"string\"},\"path\":{\"$ref\":\"#/definitions/LegacyAppPathString\"},\"type\":{\"enum\":[\"read\"],\"type\":\"string\"}},\"required\":[\"command\",\"name\",\"path\",\"type\"],\"type\":\"object\"},{\"properties\":{\"command\":{\"type\":\"string\"},\"path\":{\"type\":[\"string\",\"null\"]},\"type\":{\"enum\":[\"listFiles\"],\"type\":\"string\"}},\"required\":[\"command\",\"type\"],\"type\":\"object\"},{\"properties\":{\"command\":{\"type\":\"string\"},\"path\":{\"type\":[\"string\",\"null\"]},\"query\":{\"type\":[\"string\",\"null\"]},\"type\":{\"enum\":[\"search\"],\"type\":\"string\"}},\"required\":[\"command\",\"type\"],\"type\":\"object\"},{\"properties\":{\"command\":{\"type\":\"string\"},\"type\":{\"enum\":[\"unknown\"],\"type\":\"string\"}},\"required\":[\"command\",\"type\"],\"type\":\"object\"}]},\"CommandExecutionSource\":{\"enum\":[\"agent\",\"userShell\",\"unifiedExecStartup\",\"unifiedExecInteraction\"],\"type\":\"string\"},\"CommandExecutionStatus\":{\"enum\":[\"inProgress\",\"completed\",\"failed\",\"declined\"],\"type\":\"string\"},\"DynamicToolCallOutputContentItem\":{\"oneOf\":[{\"properties\":{\"text\":{\"type\":\"string\"},\"type\":{\"enum\":[\"inputText\"],\"type\":\"string\"}},\"required\":[\"text\",\"type\"],\"type\":\"object\"},{\"properties\":{\"imageUrl\":{\"type\":\"string\"},\"type\":{\"enum\":[\"inputImage\"],\"type\":\"string\"}},\"required\":[\"imageUrl\",\"type\"],\"type\":\"object\"},{\"properties\":{\"audioUrl\":{\"type\":\"string\"},\"type\":{\"enum\":[\"inputAudio\"],\"type\":\"string\"}},\"required\":[\"audioUrl\",\"type\"],\"type\":\"object\"}]},\"DynamicToolCallStatus\":{\"enum\":[\"inProgress\",\"completed\",\"failed\"],\"type\":\"string\"},\"FileUpdateChange\":{\"properties\":{\"diff\":{\"type\":\"string\"},\"kind\":{\"$ref\":\"#/definitions/PatchChangeKind\"},\"path\":{\"type\":\"string\"}},\"required\":[\"diff\",\"kind\",\"path\"],\"type\":\"object\"},\"FunctionCallOutputBody\":{\"anyOf\":[{\"type\":\"string\"},{\"items\":{\"$ref\":\"#/definitions/FunctionCallOutputContentItem\"},\"type\":\"array\"}]},\"FunctionCallOutputContentItem\":{\"oneOf\":[{\"properties\":{\"text\":{\"type\":\"string\"},\"type\":{\"enum\":[\"input_text\"],\"type\":\"string\"}},\"required\":[\"text\",\"type\"],\"type\":\"object\"},{\"anyOf\":[{\"properties\":{\"image_url\":{\"type\":\"string\"}},\"required\":[\"image_url\"],\"type\":\"object\"},{\"properties\":{\"file_id\":{\"type\":\"string\"}},\"required\":[\"file_id\"],\"type\":\"object\"}],\"properties\":{\"detail\":{\"anyOf\":[{\"$ref\":\"#/definitions/ImageDetail\"},{\"type\":\"null\"}]},\"type\":{\"enum\":[\"input_image\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"audio_url\":{\"type\":\"string\"},\"type\":{\"enum\":[\"input_audio\"],\"type\":\"string\"}},\"required\":[\"audio_url\",\"type\"],\"type\":\"object\"},{\"properties\":{\"encrypted_content\":{\"type\":\"string\"},\"type\":{\"enum\":[\"encrypted_content\"],\"type\":\"string\"}},\"required\":[\"encrypted_content\",\"type\"],\"type\":\"object\"}]},\"GitInfo\":{\"properties\":{\"branch\":{\"type\":[\"string\",\"null\"]},\"originUrl\":{\"type\":[\"string\",\"null\"]},\"sha\":{\"type\":[\"string\",\"null\"]}},\"type\":\"object\"},\"HookPromptFragment\":{\"properties\":{\"hookRunId\":{\"type\":\"string\"},\"text\":{\"type\":\"string\"}},\"required\":[\"hookRunId\",\"text\"],\"type\":\"object\"},\"ImageDetail\":{\"enum\":[\"auto\",\"low\",\"high\",\"original\"],\"type\":\"string\"},\"ImageGenerationFailure\":{\"oneOf\":[{\"properties\":{\"limitId\":{\"type\":\"string\"},\"resetsAt\":{\"format\":\"int64\",\"type\":[\"integer\",\"null\"]},\"type\":{\"enum\":[\"usageLimitExceeded\"],\"type\":\"string\"}},\"required\":[\"limitId\",\"type\"],\"type\":\"object\"}]},\"LegacyAppPathString\":{\"type\":\"string\"},\"McpAppDisplayMode\":{\"enum\":[\"inline\",\"fullscreen\"],\"type\":\"string\"},\"McpAppUi\":{\"properties\":{\"preferredModelDisplayMode\":{\"$ref\":\"#/definitions/McpAppDisplayMode\"},\"resourceUri\":{\"type\":\"string\"}},\"required\":[\"preferredModelDisplayMode\",\"resourceUri\"],\"type\":\"object\"},\"McpToolCallAppContext\":{\"properties\":{\"actionName\":{\"type\":[\"string\",\"null\"]},\"appName\":{\"type\":[\"string\",\"null\"]},\"connectorId\":{\"type\":\"string\"},\"linkId\":{\"type\":[\"string\",\"null\"]},\"resourceUri\":{\"type\":[\"string\",\"null\"]}},\"required\":[\"connectorId\"],\"type\":\"object\"},\"McpToolCallError\":{\"properties\":{\"message\":{\"type\":\"string\"}},\"required\":[\"message\"],\"type\":\"object\"},\"McpToolCallResult\":{\"properties\":{\"_meta\":true,\"content\":{\"items\":true,\"type\":\"array\"},\"structuredContent\":true},\"required\":[\"content\"],\"type\":\"object\"},\"McpToolCallStatus\":{\"enum\":[\"inProgress\",\"completed\",\"failed\"],\"type\":\"string\"},\"MemoryCitation\":{\"properties\":{\"entries\":{\"items\":{\"$ref\":\"#/definitions/MemoryCitationEntry\"},\"type\":\"array\"},\"threadIds\":{\"items\":{\"type\":\"string\"},\"type\":\"array\"}},\"required\":[\"entries\",\"threadIds\"],\"type\":\"object\"},\"MemoryCitationEntry\":{\"properties\":{\"lineEnd\":{\"format\":\"uint32\",\"minimum\":0.0,\"type\":\"integer\"},\"lineStart\":{\"format\":\"uint32\",\"minimum\":0.0,\"type\":\"integer\"},\"note\":{\"type\":\"string\"},\"path\":{\"type\":\"string\"}},\"required\":[\"lineEnd\",\"lineStart\",\"note\",\"path\"],\"type\":\"object\"},\"MessagePhase\":{\"oneOf\":[{\"enum\":[\"commentary\"],\"type\":\"string\"},{\"enum\":[\"final_answer\"],\"type\":\"string\"}]},\"MisalignmentErrorDetails\":{\"properties\":{\"detailedExplanation\":{\"type\":[\"string\",\"null\"]},\"errorType\":{\"type\":[\"string\",\"null\"]},\"steer\":{\"anyOf\":[{\"$ref\":\"#/definitions/MisalignmentSteer\"},{\"type\":\"null\"}]}},\"type\":\"object\"},\"MisalignmentSteer\":{\"properties\":{\"message\":{\"type\":\"string\"}},\"required\":[\"message\"],\"type\":\"object\"},\"ModeKind\":{\"enum\":[\"plan\",\"default\"],\"type\":\"string\"},\"MultiAgentMode\":{\"oneOf\":[{\"enum\":[\"explicitRequestOnly\",\"proactive\"],\"type\":\"string\"},{\"additionalProperties\":false,\"properties\":{\"custom\":{\"type\":\"string\"}},\"required\":[\"custom\"],\"type\":\"object\"}]},\"NetworkAccess\":{\"enum\":[\"restricted\",\"enabled\"],\"type\":\"string\"},\"NonSteerableTurnKind\":{\"enum\":[\"review\",\"compact\"],\"type\":\"string\"},\"PatchApplyStatus\":{\"enum\":[\"inProgress\",\"completed\",\"failed\",\"declined\"],\"type\":\"string\"},\"PatchChangeKind\":{\"oneOf\":[{\"properties\":{\"type\":{\"enum\":[\"add\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"type\":{\"enum\":[\"delete\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"move_path\":{\"type\":[\"string\",\"null\"]},\"type\":{\"enum\":[\"update\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"}]},\"QueuedSubmission\":{\"properties\":{\"clientUserMessageId\":{\"type\":\"string\"},\"id\":{\"type\":\"string\"},\"input\":{\"items\":{\"$ref\":\"#/definitions/UserInput\"},\"type\":\"array\"}},\"required\":[\"clientUserMessageId\",\"id\",\"input\"],\"type\":\"object\"},\"ReasoningEffort\":{\"minLength\":1,\"type\":\"string\"},\"RemoteControlConnectionStatus\":{\"enum\":[\"disabled\",\"connecting\",\"connected\",\"errored\"],\"type\":\"string\"},\"SandboxPolicy\":{\"oneOf\":[{\"properties\":{\"type\":{\"enum\":[\"dangerFullAccess\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"networkAccess\":{\"type\":\"boolean\"},\"type\":{\"enum\":[\"readOnly\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"networkAccess\":{\"allOf\":[{\"$ref\":\"#/definitions/NetworkAccess\"}]},\"type\":{\"enum\":[\"externalSandbox\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"excludeSlashTmp\":{\"type\":\"boolean\"},\"excludeTmpdirEnvVar\":{\"type\":\"boolean\"},\"networkAccess\":{\"type\":\"boolean\"},\"type\":{\"enum\":[\"workspaceWrite\"],\"type\":\"string\"},\"writableRoots\":{\"items\":{\"$ref\":\"#/definitions/AbsolutePathBuf\"},\"type\":\"array\"}},\"required\":[\"type\"],\"type\":\"object\"}]},\"SessionSource\":{\"oneOf\":[{\"enum\":[\"cli\",\"vscode\",\"exec\",\"appServer\",\"unknown\"],\"type\":\"string\"},{\"additionalProperties\":false,\"properties\":{\"custom\":{\"type\":\"string\"}},\"required\":[\"custom\"],\"type\":\"object\"},{\"additionalProperties\":false,\"properties\":{\"subAgent\":{\"$ref\":\"#/definitions/SubAgentSource\"}},\"required\":[\"subAgent\"],\"type\":\"object\"}]},\"Settings\":{\"properties\":{\"developer_instructions\":{\"type\":[\"string\",\"null\"]},\"model\":{\"type\":\"string\"},\"reasoning_effort\":{\"anyOf\":[{\"$ref\":\"#/definitions/ReasoningEffort\"},{\"type\":\"null\"}]}},\"required\":[\"model\"],\"type\":\"object\"},\"SubAgentActivityKind\":{\"enum\":[\"started\",\"interacted\",\"interrupted\",\"completed\"],\"type\":\"string\"},\"SubAgentSource\":{\"oneOf\":[{\"enum\":[\"review\",\"compact\",\"memory_consolidation\"],\"type\":\"string\"},{\"additionalProperties\":false,\"properties\":{\"thread_spawn\":{\"properties\":{\"agent_nickname\":{\"type\":[\"string\",\"null\"]},\"agent_path\":{\"anyOf\":[{\"$ref\":\"#/definitions/AgentPath\"},{\"type\":\"null\"}]},\"agent_role\":{\"type\":[\"string\",\"null\"]},\"depth\":{\"format\":\"int32\",\"type\":\"integer\"},\"parent_thread_id\":{\"$ref\":\"#/definitions/ThreadId\"}},\"required\":[\"depth\",\"parent_thread_id\"],\"type\":\"object\"}},\"required\":[\"thread_spawn\"],\"type\":\"object\"},{\"additionalProperties\":false,\"properties\":{\"other\":{\"type\":\"string\"}},\"required\":[\"other\"],\"type\":\"object\"}]},\"TextElement\":{\"properties\":{\"byteRange\":{\"allOf\":[{\"$ref\":\"#/definitions/ByteRange\"}]},\"placeholder\":{\"type\":[\"string\",\"null\"]}},\"required\":[\"byteRange\"],\"type\":\"object\"},\"Thread\":{\"properties\":{\"agentNickname\":{\"type\":[\"string\",\"null\"]},\"agentRole\":{\"type\":[\"string\",\"null\"]},\"canAcceptDirectInput\":{\"type\":[\"boolean\",\"null\"]},\"cliVersion\":{\"type\":\"string\"},\"createdAt\":{\"format\":\"int64\",\"type\":\"integer\"},\"cwd\":{\"allOf\":[{\"$ref\":\"#/definitions/AbsolutePathBuf\"}]},\"daybreakEnabled\":{\"type\":[\"boolean\",\"null\"]},\"environments\":{\"items\":{\"$ref\":\"#/definitions/ThreadEnvironment\"},\"type\":[\"array\",\"null\"]},\"ephemeral\":{\"type\":\"boolean\"},\"extra\":{\"anyOf\":[{\"$ref\":\"#/definitions/ThreadExtra\"},{\"type\":\"null\"}]},\"forkedFromId\":{\"type\":[\"string\",\"null\"]},\"gitInfo\":{\"anyOf\":[{\"$ref\":\"#/definitions/GitInfo\"},{\"type\":\"null\"}]},\"historyMode\":{\"allOf\":[{\"$ref\":\"#/definitions/ThreadHistoryMode\"}]},\"id\":{\"type\":\"string\"},\"model\":{\"type\":[\"string\",\"null\"]},\"modelProvider\":{\"type\":\"string\"},\"name\":{\"type\":[\"string\",\"null\"]},\"originator\":{\"type\":[\"string\",\"null\"]},\"parentThreadId\":{\"type\":[\"string\",\"null\"]},\"path\":{\"type\":[\"string\",\"null\"]},\"preview\":{\"type\":\"string\"},\"projectId\":{\"type\":[\"string\",\"null\"]},\"reasoningEffort\":{\"anyOf\":[{\"$ref\":\"#/definitions/ReasoningEffort\"},{\"type\":\"null\"}]},\"recencyAt\":{\"format\":\"int64\",\"type\":[\"integer\",\"null\"]},\"section\":{\"anyOf\":[{\"$ref\":\"#/definitions/ThreadSection\"},{\"type\":\"null\"}]},\"sectionEnteredAt\":{\"format\":\"int64\",\"type\":[\"integer\",\"null\"]},\"sessionId\":{\"type\":\"string\"},\"source\":{\"allOf\":[{\"$ref\":\"#/definitions/SessionSource\"}]},\"status\":{\"allOf\":[{\"$ref\":\"#/definitions/ThreadStatus\"}]},\"threadSource\":{\"anyOf\":[{\"$ref\":\"#/definitions/ThreadSource\"},{\"type\":\"null\"}]},\"turns\":{\"items\":{\"$ref\":\"#/definitions/Turn\"},\"type\":\"array\"},\"updatedAt\":{\"format\":\"int64\",\"type\":\"integer\"}},\"required\":[\"cliVersion\",\"createdAt\",\"cwd\",\"ephemeral\",\"id\",\"modelProvider\",\"preview\",\"projectId\",\"sessionId\",\"source\",\"status\",\"turns\",\"updatedAt\"],\"type\":\"object\"},\"ThreadActiveFlag\":{\"enum\":[\"waitingOnApproval\",\"waitingOnUserInput\"],\"type\":\"string\"},\"ThreadBackgroundTerminal\":{\"properties\":{\"command\":{\"type\":\"string\"},\"cpuPercent\":{\"format\":\"double\",\"type\":[\"number\",\"null\"]},\"cwd\":{\"$ref\":\"#/definitions/LegacyAppPathString\"},\"itemId\":{\"type\":\"string\"},\"osPid\":{\"format\":\"uint32\",\"minimum\":0.0,\"type\":[\"integer\",\"null\"]},\"processId\":{\"type\":\"string\"},\"rssKb\":{\"format\":\"uint64\",\"minimum\":0.0,\"type\":[\"integer\",\"null\"]}},\"required\":[\"command\",\"cwd\",\"itemId\",\"processId\"],\"type\":\"object\"},\"ThreadEnvironment\":{\"properties\":{\"cwd\":{\"$ref\":\"#/definitions/LegacyAppPathString\"},\"environmentId\":{\"type\":\"string\"},\"runtimeWorkspaceRoots\":{\"items\":{\"$ref\":\"#/definitions/LegacyAppPathString\"},\"type\":\"array\"}},\"required\":[\"cwd\",\"environmentId\",\"runtimeWorkspaceRoots\"],\"type\":\"object\"},\"ThreadExtra\":{\"type\":\"object\"},\"ThreadHistoryMode\":{\"enum\":[\"legacy\",\"paginated\"],\"type\":\"string\"},\"ThreadId\":{\"type\":\"string\"},\"ThreadItem\":{\"oneOf\":[{\"properties\":{\"clientId\":{\"type\":[\"string\",\"null\"]},\"content\":{\"items\":{\"$ref\":\"#/definitions/UserInput\"},\"type\":\"array\"},\"id\":{\"type\":\"string\"},\"type\":{\"enum\":[\"userMessage\"],\"type\":\"string\"}},\"required\":[\"content\",\"id\",\"type\"],\"type\":\"object\"},{\"properties\":{\"fragments\":{\"items\":{\"$ref\":\"#/definitions/HookPromptFragment\"},\"type\":\"array\"},\"id\":{\"type\":\"string\"},\"type\":{\"enum\":[\"hookPrompt\"],\"type\":\"string\"}},\"required\":[\"fragments\",\"id\",\"type\"],\"type\":\"object\"},{\"properties\":{\"delivery\":{\"anyOf\":[{\"$ref\":\"#/definitions/AgentMessageDelivery\"},{\"type\":\"null\"}]},\"id\":{\"type\":\"string\"},\"memoryCitation\":{\"anyOf\":[{\"$ref\":\"#/definitions/MemoryCitation\"},{\"type\":\"null\"}]},\"phase\":{\"anyOf\":[{\"$ref\":\"#/definitions/MessagePhase\"},{\"type\":\"null\"}]},\"questions\":{\"items\":{\"$ref\":\"#/definitions/AsyncUserInputQuestion\"},\"type\":[\"array\",\"null\"]},\"text\":{\"type\":\"string\"},\"type\":{\"enum\":[\"agentMessage\"],\"type\":\"string\"}},\"required\":[\"id\",\"text\",\"type\"],\"type\":\"object\"},{\"properties\":{\"id\":{\"type\":\"string\"},\"name\":{\"type\":\"string\"},\"namespace\":{\"type\":[\"string\",\"null\"]},\"output\":{\"$ref\":\"#/definitions/FunctionCallOutputBody\"},\"type\":{\"enum\":[\"functionCallOutput\"],\"type\":\"string\"}},\"required\":[\"id\",\"name\",\"output\",\"type\"],\"type\":\"object\"},{\"properties\":{\"id\":{\"type\":\"string\"},\"text\":{\"type\":\"string\"},\"type\":{\"enum\":[\"plan\"],\"type\":\"string\"}},\"required\":[\"id\",\"text\",\"type\"],\"type\":\"object\"},{\"properties\":{\"content\":{\"items\":{\"type\":\"string\"},\"type\":\"array\"},\"id\":{\"type\":\"string\"},\"summary\":{\"items\":{\"type\":\"string\"},\"type\":\"array\"},\"type\":{\"enum\":[\"reasoning\"],\"type\":\"string\"}},\"required\":[\"id\",\"type\"],\"type\":\"object\"},{\"properties\":{\"aggregatedOutput\":{\"type\":[\"string\",\"null\"]},\"command\":{\"type\":\"string\"},\"commandActions\":{\"items\":{\"$ref\":\"#/definitions/CommandAction\"},\"type\":\"array\"},\"cwd\":{\"allOf\":[{\"$ref\":\"#/definitions/LegacyAppPathString\"}]},\"durationMs\":{\"format\":\"int64\",\"type\":[\"integer\",\"null\"]},\"exitCode\":{\"format\":\"int32\",\"type\":[\"integer\",\"null\"]},\"id\":{\"type\":\"string\"},\"pluginId\":{\"type\":[\"string\",\"null\"]},\"processId\":{\"type\":[\"string\",\"null\"]},\"scriptPath\":{\"type\":[\"string\",\"null\"]},\"source\":{\"allOf\":[{\"$ref\":\"#/definitions/CommandExecutionSource\"}]},\"status\":{\"$ref\":\"#/definitions/CommandExecutionStatus\"},\"type\":{\"enum\":[\"commandExecution\"],\"type\":\"string\"}},\"required\":[\"command\",\"commandActions\",\"cwd\",\"id\",\"status\",\"type\"],\"type\":\"object\"},{\"properties\":{\"changes\":{\"items\":{\"$ref\":\"#/definitions/FileUpdateChange\"},\"type\":\"array\"},\"id\":{\"type\":\"string\"},\"status\":{\"$ref\":\"#/definitions/PatchApplyStatus\"},\"type\":{\"enum\":[\"fileChange\"],\"type\":\"string\"}},\"required\":[\"changes\",\"id\",\"status\",\"type\"],\"type\":\"object\"},{\"properties\":{\"appContext\":{\"anyOf\":[{\"$ref\":\"#/definitions/McpToolCallAppContext\"},{\"type\":\"null\"}]},\"arguments\":true,\"durationMs\":{\"format\":\"int64\",\"type\":[\"integer\",\"null\"]},\"error\":{\"anyOf\":[{\"$ref\":\"#/definitions/McpToolCallError\"},{\"type\":\"null\"}]},\"id\":{\"type\":\"string\"},\"mcpAppResourceUri\":{\"type\":[\"string\",\"null\"]},\"mcpAppUi\":{\"anyOf\":[{\"$ref\":\"#/definitions/McpAppUi\"},{\"type\":\"null\"}]},\"pluginId\":{\"type\":[\"string\",\"null\"]},\"readOnlyHint\":{\"type\":[\"boolean\",\"null\"]},\"result\":{\"anyOf\":[{\"$ref\":\"#/definitions/McpToolCallResult\"},{\"type\":\"null\"}]},\"server\":{\"type\":\"string\"},\"status\":{\"$ref\":\"#/definitions/McpToolCallStatus\"},\"tool\":{\"type\":\"string\"},\"type\":{\"enum\":[\"mcpToolCall\"],\"type\":\"string\"}},\"required\":[\"arguments\",\"id\",\"server\",\"status\",\"tool\",\"type\"],\"type\":\"object\"},{\"properties\":{\"arguments\":true,\"contentItems\":{\"items\":{\"$ref\":\"#/definitions/DynamicToolCallOutputContentItem\"},\"type\":[\"array\",\"null\"]},\"durationMs\":{\"format\":\"int64\",\"type\":[\"integer\",\"null\"]},\"id\":{\"type\":\"string\"},\"namespace\":{\"type\":[\"string\",\"null\"]},\"status\":{\"$ref\":\"#/definitions/DynamicToolCallStatus\"},\"success\":{\"type\":[\"boolean\",\"null\"]},\"tool\":{\"type\":\"string\"},\"type\":{\"enum\":[\"dynamicToolCall\"],\"type\":\"string\"}},\"required\":[\"arguments\",\"id\",\"status\",\"tool\",\"type\"],\"type\":\"object\"},{\"properties\":{\"agentsStates\":{\"additionalProperties\":{\"$ref\":\"#/definitions/CollabAgentState\"},\"type\":\"object\"},\"id\":{\"type\":\"string\"},\"model\":{\"type\":[\"string\",\"null\"]},\"prompt\":{\"type\":[\"string\",\"null\"]},\"reasoningEffort\":{\"anyOf\":[{\"$ref\":\"#/definitions/ReasoningEffort\"},{\"type\":\"null\"}]},\"receiverThreadIds\":{\"items\":{\"type\":\"string\"},\"type\":\"array\"},\"senderThreadId\":{\"type\":\"string\"},\"status\":{\"allOf\":[{\"$ref\":\"#/definitions/CollabAgentToolCallStatus\"}]},\"tool\":{\"allOf\":[{\"$ref\":\"#/definitions/CollabAgentTool\"}]},\"type\":{\"enum\":[\"collabAgentToolCall\"],\"type\":\"string\"}},\"required\":[\"agentsStates\",\"id\",\"receiverThreadIds\",\"senderThreadId\",\"status\",\"tool\",\"type\"],\"type\":\"object\"},{\"properties\":{\"agentPath\":{\"type\":\"string\"},\"agentThreadId\":{\"type\":\"string\"},\"id\":{\"type\":\"string\"},\"kind\":{\"$ref\":\"#/definitions/SubAgentActivityKind\"},\"type\":{\"enum\":[\"subAgentActivity\"],\"type\":\"string\"}},\"required\":[\"agentPath\",\"agentThreadId\",\"id\",\"kind\",\"type\"],\"type\":\"object\"},{\"properties\":{\"action\":{\"anyOf\":[{\"$ref\":\"#/definitions/WebSearchAction\"},{\"type\":\"null\"}]},\"id\":{\"type\":\"string\"},\"query\":{\"type\":\"string\"},\"results\":{\"items\":true,\"type\":[\"array\",\"null\"]},\"type\":{\"enum\":[\"webSearch\"],\"type\":\"string\"}},\"required\":[\"id\",\"query\",\"type\"],\"type\":\"object\"},{\"properties\":{\"id\":{\"type\":\"string\"},\"path\":{\"$ref\":\"#/definitions/LegacyAppPathString\"},\"type\":{\"enum\":[\"imageView\"],\"type\":\"string\"}},\"required\":[\"id\",\"path\",\"type\"],\"type\":\"object\"},{\"properties\":{\"durationMs\":{\"format\":\"uint64\",\"minimum\":0.0,\"type\":\"integer\"},\"id\":{\"type\":\"string\"},\"type\":{\"enum\":[\"sleep\"],\"type\":\"string\"}},\"required\":[\"durationMs\",\"id\",\"type\"],\"type\":\"object\"},{\"properties\":{\"failure\":{\"anyOf\":[{\"$ref\":\"#/definitions/ImageGenerationFailure\"},{\"type\":\"null\"}]},\"id\":{\"type\":\"string\"},\"result\":{\"type\":\"string\"},\"revisedPrompt\":{\"type\":[\"string\",\"null\"]},\"savedPath\":{\"anyOf\":[{\"$ref\":\"#/definitions/AbsolutePathBuf\"},{\"type\":\"null\"}]},\"status\":{\"type\":\"string\"},\"transparentBackground\":{\"type\":[\"boolean\",\"null\"]},\"type\":{\"enum\":[\"imageGeneration\"],\"type\":\"string\"}},\"required\":[\"id\",\"result\",\"status\",\"type\"],\"type\":\"object\"},{\"properties\":{\"id\":{\"type\":\"string\"},\"review\":{\"type\":\"string\"},\"type\":{\"enum\":[\"enteredReviewMode\"],\"type\":\"string\"}},\"required\":[\"id\",\"review\",\"type\"],\"type\":\"object\"},{\"properties\":{\"id\":{\"type\":\"string\"},\"review\":{\"type\":\"string\"},\"type\":{\"enum\":[\"exitedReviewMode\"],\"type\":\"string\"}},\"required\":[\"id\",\"review\",\"type\"],\"type\":\"object\"},{\"properties\":{\"id\":{\"type\":\"string\"},\"type\":{\"enum\":[\"contextCompaction\"],\"type\":\"string\"}},\"required\":[\"id\",\"type\"],\"type\":\"object\"}]},\"ThreadSection\":{\"properties\":{\"appearance\":{\"anyOf\":[{\"$ref\":\"#/definitions/ThreadSectionAppearance\"},{\"type\":\"null\"}]},\"id\":{\"type\":\"string\"},\"name\":{\"type\":\"string\"}},\"required\":[\"id\",\"name\"],\"type\":\"object\"},\"ThreadSectionAppearance\":{\"properties\":{\"color\":{\"type\":[\"string\",\"null\"]},\"icon\":{\"type\":[\"string\",\"null\"]}},\"type\":\"object\"},\"ThreadSource\":{\"type\":\"string\"},\"ThreadStatus\":{\"oneOf\":[{\"properties\":{\"type\":{\"enum\":[\"notLoaded\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"type\":{\"enum\":[\"idle\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"type\":{\"enum\":[\"systemError\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"activeFlags\":{\"items\":{\"$ref\":\"#/definitions/ThreadActiveFlag\"},\"type\":\"array\"},\"type\":{\"enum\":[\"active\"],\"type\":\"string\"}},\"required\":[\"activeFlags\",\"type\"],\"type\":\"object\"}]},\"Turn\":{\"properties\":{\"completedAt\":{\"format\":\"int64\",\"type\":[\"integer\",\"null\"]},\"durationMs\":{\"format\":\"int64\",\"type\":[\"integer\",\"null\"]},\"error\":{\"anyOf\":[{\"$ref\":\"#/definitions/TurnError\"},{\"type\":\"null\"}]},\"id\":{\"type\":\"string\"},\"items\":{\"items\":{\"$ref\":\"#/definitions/ThreadItem\"},\"type\":\"array\"},\"itemsView\":{\"allOf\":[{\"$ref\":\"#/definitions/TurnItemsView\"}]},\"startedAt\":{\"format\":\"int64\",\"type\":[\"integer\",\"null\"]},\"status\":{\"$ref\":\"#/definitions/TurnStatus\"}},\"required\":[\"id\",\"items\",\"status\"],\"type\":\"object\"},\"TurnError\":{\"properties\":{\"additionalDetails\":{\"type\":[\"string\",\"null\"]},\"codexErrorInfo\":{\"anyOf\":[{\"$ref\":\"#/definitions/CodexErrorInfo\"},{\"type\":\"null\"}]},\"message\":{\"type\":\"string\"},\"misalignment\":{\"anyOf\":[{\"$ref\":\"#/definitions/MisalignmentErrorDetails\"},{\"type\":\"null\"}]}},\"required\":[\"message\"],\"type\":\"object\"},\"TurnItemsView\":{\"oneOf\":[{\"enum\":[\"notLoaded\"],\"type\":\"string\"},{\"enum\":[\"summary\"],\"type\":\"string\"},{\"enum\":[\"full\"],\"type\":\"string\"}]},\"TurnStatus\":{\"enum\":[\"completed\",\"interrupted\",\"failed\",\"inProgress\"],\"type\":\"string\"},\"TurnsPage\":{\"properties\":{\"backwardsCursor\":{\"type\":[\"string\",\"null\"]},\"data\":{\"items\":{\"$ref\":\"#/definitions/Turn\"},\"type\":\"array\"},\"nextCursor\":{\"type\":[\"string\",\"null\"]}},\"required\":[\"data\"],\"type\":\"object\"},\"UserInput\":{\"oneOf\":[{\"properties\":{\"text\":{\"type\":\"string\"},\"text_elements\":{\"items\":{\"$ref\":\"#/definitions/TextElement\"},\"type\":\"array\"},\"type\":{\"enum\":[\"text\"],\"type\":\"string\"}},\"required\":[\"text\",\"type\"],\"type\":\"object\"},{\"anyOf\":[{\"properties\":{\"url\":{\"type\":\"string\"}},\"required\":[\"url\"],\"type\":\"object\"},{\"properties\":{\"fileId\":{\"type\":\"string\"}},\"required\":[\"fileId\"],\"type\":\"object\"}],\"properties\":{\"detail\":{\"anyOf\":[{\"$ref\":\"#/definitions/ImageDetail\"},{\"type\":\"null\"}]},\"type\":{\"enum\":[\"image\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"detail\":{\"anyOf\":[{\"$ref\":\"#/definitions/ImageDetail\"},{\"type\":\"null\"}]},\"path\":{\"type\":\"string\"},\"type\":{\"enum\":[\"localImage\"],\"type\":\"string\"}},\"required\":[\"path\",\"type\"],\"type\":\"object\"},{\"properties\":{\"type\":{\"enum\":[\"audio\"],\"type\":\"string\"},\"url\":{\"type\":\"string\"}},\"required\":[\"type\",\"url\"],\"type\":\"object\"},{\"properties\":{\"path\":{\"type\":\"string\"},\"type\":{\"enum\":[\"localAudio\"],\"type\":\"string\"}},\"required\":[\"path\",\"type\"],\"type\":\"object\"},{\"properties\":{\"name\":{\"type\":\"string\"},\"path\":{\"type\":\"string\"},\"type\":{\"enum\":[\"skill\"],\"type\":\"string\"}},\"required\":[\"name\",\"path\",\"type\"],\"type\":\"object\"},{\"properties\":{\"name\":{\"type\":\"string\"},\"path\":{\"type\":\"string\"},\"type\":{\"enum\":[\"mention\"],\"type\":\"string\"}},\"required\":[\"name\",\"path\",\"type\"],\"type\":\"object\"}]},\"WebSearchAction\":{\"oneOf\":[{\"properties\":{\"queries\":{\"items\":{\"type\":\"string\"},\"type\":[\"array\",\"null\"]},\"query\":{\"type\":[\"string\",\"null\"]},\"type\":{\"enum\":[\"search\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"type\":{\"enum\":[\"openPage\"],\"type\":\"string\"},\"url\":{\"type\":[\"string\",\"null\"]}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"pattern\":{\"type\":[\"string\",\"null\"]},\"type\":{\"enum\":[\"findInPage\"],\"type\":\"string\"},\"url\":{\"type\":[\"string\",\"null\"]}},\"required\":[\"type\"],\"type\":\"object\"},{\"properties\":{\"type\":{\"enum\":[\"other\"],\"type\":\"string\"}},\"required\":[\"type\"],\"type\":\"object\"}]}},\"roots\":{\"InitializeResponse\":{\"properties\":{\"codexHome\":{\"allOf\":[{\"$ref\":\"#/definitions/AbsolutePathBuf\"}]},\"platformFamily\":{\"type\":\"string\"},\"platformOs\":{\"type\":\"string\"},\"userAgent\":{\"type\":\"string\"}},\"required\":[\"codexHome\",\"platformFamily\",\"platformOs\",\"userAgent\"],\"type\":\"object\"},\"RemoteControlStatusChangedNotification\":{\"properties\":{\"environmentId\":{\"type\":[\"string\",\"null\"]},\"installationId\":{\"type\":\"string\"},\"serverName\":{\"type\":\"string\"},\"status\":{\"$ref\":\"#/definitions/RemoteControlConnectionStatus\"}},\"required\":[\"installationId\",\"serverName\",\"status\"],\"type\":\"object\"},\"ThreadBackgroundTerminalsListResponse\":{\"properties\":{\"data\":{\"items\":{\"$ref\":\"#/definitions/ThreadBackgroundTerminal\"},\"type\":\"array\"},\"nextCursor\":{\"type\":[\"string\",\"null\"]}},\"required\":[\"data\"],\"type\":\"object\"},\"ThreadLoadedListResponse\":{\"properties\":{\"data\":{\"items\":{\"type\":\"string\"},\"type\":\"array\"},\"nextCursor\":{\"type\":[\"string\",\"null\"]}},\"required\":[\"data\"],\"type\":\"object\"},\"ThreadQueueChangedNotification\":{\"properties\":{\"threadId\":{\"type\":\"string\"}},\"required\":[\"threadId\"],\"type\":\"object\"},\"ThreadQueueListResponse\":{\"properties\":{\"data\":{\"items\":{\"$ref\":\"#/definitions/QueuedSubmission\"},\"type\":\"array\"},\"nextCursor\":{\"type\":[\"string\",\"null\"]}},\"required\":[\"data\"],\"type\":\"object\"},\"ThreadReadResponse\":{\"properties\":{\"thread\":{\"$ref\":\"#/definitions/Thread\"}},\"required\":[\"thread\"],\"type\":\"object\"},\"ThreadResumeResponse\":{\"properties\":{\"activePermissionProfile\":{\"anyOf\":[{\"$ref\":\"#/definitions/ActivePermissionProfile\"},{\"type\":\"null\"}]},\"approvalPolicy\":{\"$ref\":\"#/definitions/AskForApproval\"},\"approvalsReviewer\":{\"allOf\":[{\"$ref\":\"#/definitions/ApprovalsReviewer\"}]},\"collaborationMode\":{\"anyOf\":[{\"$ref\":\"#/definitions/CollaborationMode\"},{\"type\":\"null\"}]},\"cwd\":{\"$ref\":\"#/definitions/AbsolutePathBuf\"},\"disabledPluginIds\":{\"items\":{\"type\":\"string\"},\"type\":\"array\"},\"initialTurnsPage\":{\"anyOf\":[{\"$ref\":\"#/definitions/TurnsPage\"},{\"type\":\"null\"}]},\"instructionSources\":{\"items\":{\"$ref\":\"#/definitions/LegacyAppPathString\"},\"type\":\"array\"},\"itemsBackwardsCursor\":{\"type\":[\"string\",\"null\"]},\"model\":{\"type\":\"string\"},\"modelProvider\":{\"type\":\"string\"},\"multiAgentMode\":{\"allOf\":[{\"$ref\":\"#/definitions/MultiAgentMode\"}]},\"reasoningEffort\":{\"anyOf\":[{\"$ref\":\"#/definitions/ReasoningEffort\"},{\"type\":\"null\"}]},\"runtimeWorkspaceRoots\":{\"items\":{\"$ref\":\"#/definitions/AbsolutePathBuf\"},\"type\":\"array\"},\"sandbox\":{\"allOf\":[{\"$ref\":\"#/definitions/SandboxPolicy\"}]},\"serviceTier\":{\"type\":[\"string\",\"null\"]},\"thread\":{\"$ref\":\"#/definitions/Thread\"},\"turnsBackwardsCursor\":{\"type\":[\"string\",\"null\"]}},\"required\":[\"approvalPolicy\",\"approvalsReviewer\",\"cwd\",\"model\",\"modelProvider\",\"sandbox\",\"thread\"],\"type\":\"object\"},\"ThreadStartedNotification\":{\"properties\":{\"thread\":{\"$ref\":\"#/definitions/Thread\"}},\"required\":[\"thread\"],\"type\":\"object\"},\"ThreadStatusChangedNotification\":{\"properties\":{\"status\":{\"$ref\":\"#/definitions/ThreadStatus\"},\"threadId\":{\"type\":\"string\"}},\"required\":[\"status\",\"threadId\"],\"type\":\"object\"}}}"
