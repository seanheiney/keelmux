package tools

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

const protocolVersion = "2025-11-25"
const maxFrame = 256 << 10
const maxBytes = 1 << 20
const maxFrames = 32
const maxPages = 4
const maxTools = 256

var toolName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// These published versions share this client's initialize/initialized and
// paginated tools/list subset. No version grants optional client capabilities.
func supportedProtocol(version string) bool {
	switch version {
	case "2024-11-05", "2025-03-26", "2025-06-18", protocolVersion:
		return true
	}
	return false
}

type protocol struct {
	in            io.Writer
	out           *bufio.Reader
	frames, bytes int
}

func handshake(in io.Writer, out io.Reader, r *Receipt) string {
	p := protocol{in: in, out: bufio.NewReaderSize(out, maxFrame+1)}
	if err := p.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "keelmux-health", "version": "1"},
	}}); err != nil {
		return "write_failed"
	}
	result, code := p.response(1)
	if code != "" {
		return code
	}
	var init struct {
		ProtocolVersion string                         `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage     `json:"capabilities"`
		ServerInfo      struct{ Name, Version string } `json:"serverInfo"`
	}
	if json.Unmarshal(result, &init) != nil || !supportedProtocol(init.ProtocolVersion) {
		return "protocol_unqualified"
	}
	if init.ServerInfo.Name == "" || init.ServerInfo.Version == "" {
		return "response_invalid"
	}
	if !validCapabilities(init.Capabilities) {
		return "capabilities_unqualified"
	}
	r.Initialized, r.ProtocolVersion = true, init.ProtocolVersion
	if p.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}) != nil {
		return "write_failed"
	}
	seenTools, seenCursors := map[string]bool{}, map[string]bool{}
	cursor := ""
	for page := 0; page < maxPages; page++ {
		params := map[string]string{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if p.send(map[string]any{"jsonrpc": "2.0", "id": page + 2, "method": "tools/list", "params": params}) != nil {
			return "write_failed"
		}
		result, code = p.response(page + 2)
		if code != "" {
			return code
		}
		var list struct {
			Tools []struct {
				Name        string                     `json:"name"`
				InputSchema map[string]json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(result, &list) != nil || list.Tools == nil || len(list.NextCursor) > 1024 {
			return "response_invalid"
		}
		for _, tool := range list.Tools {
			var kind string
			if !toolName.MatchString(tool.Name) || seenTools[tool.Name] || json.Unmarshal(tool.InputSchema["type"], &kind) != nil || kind != "object" {
				return "response_invalid"
			}
			seenTools[tool.Name] = true
			if len(seenTools) > maxTools {
				return "tools_limit"
			}
		}
		if list.NextCursor == "" {
			r.ToolsListed, r.ToolCount = true, len(seenTools)
			return "health_ok"
		}
		if seenCursors[list.NextCursor] {
			return "pagination_repeated"
		}
		seenCursors[list.NextCursor] = true
		cursor = list.NextCursor
	}
	return "pagination_limit"
}

func validCapabilities(capabilities map[string]json.RawMessage) bool {
	if capabilities["tools"] == nil {
		return false
	}
	for name, raw := range capabilities {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return false
		}
		switch name {
		case "tools", "prompts":
			for key, value := range object {
				if key != "listChanged" || !jsonBoolean(value) {
					return false
				}
			}
		case "resources":
			for key, value := range object {
				if (key != "listChanged" && key != "subscribe") || !jsonBoolean(value) {
					return false
				}
			}
		case "logging", "completions", "experimental":
			if len(object) != 0 {
				return false
			}
		default:
			return false // No sampling, roots, elicitation, tasks or unknown policy.
		}
	}
	return true
}

func jsonBoolean(raw []byte) bool {
	value := string(bytes.TrimSpace(raw))
	return value == "true" || value == "false"
}

func (p *protocol) send(message any) error {
	b, err := json.Marshal(message)
	if err != nil || len(b) > 4096 {
		return errors.New("request_invalid")
	}
	b = append(b, '\n')
	n, err := p.in.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}

func (p *protocol) response(id int) (json.RawMessage, string) {
	for {
		line, err := p.out.ReadSlice('\n')
		p.frames++
		p.bytes += len(line)
		if len(line) > maxFrame || p.bytes > maxBytes || p.frames > maxFrames {
			return nil, "response_limit"
		}
		if err != nil {
			return nil, "read_failed"
		}
		if !strictJSON(line) {
			return nil, "response_invalid"
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(line, &envelope) != nil || envelope == nil {
			return nil, "response_invalid"
		}
		var version string
		if json.Unmarshal(envelope["jsonrpc"], &version) != nil || version != "2.0" {
			return nil, "response_invalid"
		}
		if envelope["method"] != nil {
			var method string
			// Logging is discarded, never shown or followed. Every other server
			// request/notification is outside this health-only client's policy.
			if json.Unmarshal(envelope["method"], &method) != nil || method != "notifications/message" || envelope["id"] != nil || envelope["result"] != nil || envelope["error"] != nil {
				return nil, "server_message_refused"
			}
			if !onlyKeys(envelope, "jsonrpc", "method", "params") {
				return nil, "response_invalid"
			}
			var params map[string]json.RawMessage
			if json.Unmarshal(envelope["params"], &params) != nil || params == nil {
				return nil, "response_invalid"
			}
			continue
		}
		if !onlyKeys(envelope, "jsonrpc", "id", "result", "error") || string(envelope["id"]) != fmt.Sprint(id) {
			return nil, "response_id_mismatch"
		}
		result, hasResult := envelope["result"]
		_, hasError := envelope["error"]
		if hasResult == hasError {
			return nil, "response_invalid"
		}
		if hasError {
			return nil, "server_error"
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(result, &object) != nil || object == nil {
			return nil, "response_invalid"
		}
		if id == 1 && !onlyKeys(object, "protocolVersion", "capabilities", "serverInfo", "instructions", "_meta") {
			return nil, "response_invalid"
		}
		if id != 1 && !onlyKeys(object, "tools", "nextCursor", "_meta") {
			return nil, "response_invalid"
		}
		return result, ""
	}
}

func onlyKeys(object map[string]json.RawMessage, allowed ...string) bool {
	for key := range object {
		found := false
		for _, a := range allowed {
			if a == key {
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

// encoding/json accepts duplicate keys and invalid UTF-8 by default. Neither is
// usable evidence of an exact response or policy. Bound nesting independently.
func strictJSON(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 32 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					text, ok := key.(string)
					if err != nil || !ok || seen[text] || !value(depth+1) {
						return false
					}
					seen[text] = true
				}
				end, err := d.Token()
				return err == nil && end == json.Delim('}')
			case '[':
				for d.More() {
					if !value(depth + 1) {
						return false
					}
				}
				end, err := d.Token()
				return err == nil && end == json.Delim(']')
			default:
				return false
			}
		}
		return true
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
