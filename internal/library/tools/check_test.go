//go:build darwin || linux

package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/library"
)

func fixture(t *testing.T, mode string) (library.ResolvedTool, Options, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	selected := library.ResolvedTool{PackageID: "fixture", PackageDigest: strings.Repeat("a", 64), Tool: library.Tool{
		ID: "mcp", Transport: "stdio", Command: []string{exe, "-test.run=^TestMCPFixtureProcess$", "--", mode}, EnvRefs: []string{"KEELMUX_MCP_FIXTURE", "SELECTED_SECRET"},
	}}
	lookup := func(key string) (string, bool) {
		switch key {
		case "PATH":
			return "/usr/bin:/bin", true
		case "KEELMUX_MCP_FIXTURE":
			return "1", true
		case "SELECTED_SECRET":
			return "SECRET_MUST_NOT_ESCAPE", true
		}
		return "", false
	}
	return selected, Options{Health: true, WorkingDirectory: dir, Timeout: 3 * time.Second, LookupEnv: lookup}, dir
}

func TestAvailabilityDoesNotStartReadVersionOrCreateState(t *testing.T) {
	s, o, dir := fixture(t, "healthy")
	o.Health = false
	r := Check(context.Background(), s, o)
	if !r.Declared || r.Availability != "available" || r.Health != "not_checked" || r.Started || r.Invoked || r.Cleanup != "not_needed" {
		t.Fatalf("availability overclaim: %+v", r)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("availability executed or wrote state")
	}
	s.Tool.Command[0] = filepath.Join(dir, "not-installed")
	r = Check(context.Background(), s, o)
	if !r.Declared || r.Availability != "unavailable" || r.Code != "executable_unavailable" || r.Started {
		t.Fatalf("missing executable: %+v", r)
	}
}

func TestHealthOnlyInitializesAndListsWithPrivateEnvironment(t *testing.T) {
	t.Setenv("AMBIENT_SECRET", "AMBIENT_MUST_NOT_ESCAPE")
	s, o, dir := fixture(t, "healthy")
	s.Tool.Command = append(s.Tool.Command, "$literal `argv` ; unchanged")
	r := Check(context.Background(), s, o)
	if !r.Declared || r.Availability != "available" || r.Health != "healthy" || !r.Started || !r.Initialized || !r.ToolsListed || r.ToolCount != 2 || r.ProtocolVersion != protocolVersion || r.Invoked || r.Cleanup != "complete" {
		t.Fatalf("health: %+v", r)
	}
	b, err := os.ReadFile(filepath.Join(dir, "requests"))
	if err != nil {
		t.Fatal(err)
	}
	var messages []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			t.Fatal("invalid client request")
		}
		messages = append(messages, m)
	}
	if len(messages) != 3 || messages[0]["method"] != "initialize" || messages[1]["method"] != "notifications/initialized" || messages[2]["method"] != "tools/list" {
		t.Fatalf("unexpected client methods: %s", b)
	}
	params := messages[0]["params"].(map[string]any)
	if len(params["capabilities"].(map[string]any)) != 0 {
		t.Fatal("client exposed capabilities")
	}
	private, err := os.ReadFile(filepath.Join(dir, "private-root"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(string(private)); !os.IsNotExist(err) {
		t.Fatal("private home not removed")
	}
	receipt, _ := json.Marshal(r)
	if strings.Contains(string(receipt), "SECRET") || strings.Contains(string(receipt), "PRIVATE") || strings.Contains(string(receipt), dir) {
		t.Fatal("private tool output leaked")
	}
}

func TestProtocolFailuresAlwaysStopOwnedChild(t *testing.T) {
	for _, tc := range []struct{ mode, code string }{
		{"wrong-id", "response_id_mismatch"}, {"string-id", "response_id_mismatch"}, {"duplicate-id", "response_invalid"},
		{"malformed", "response_invalid"}, {"oversized", "response_limit"}, {"unknown-version", "protocol_unqualified"},
		{"byte-limit", "response_limit"}, {"tool-limit", "tools_limit"},
		{"sampling", "server_message_refused"}, {"elicitation", "server_message_refused"}, {"roots", "server_message_refused"},
		{"unknown-policy", "capabilities_unqualified"}, {"server-error", "server_error"}, {"notifications", "response_limit"},
		{"repeated-cursor", "pagination_repeated"}, {"infinite-pages", "pagination_limit"}, {"duplicate-tools", "response_invalid"},
		{"invalid-schema", "response_invalid"}, {"unknown-result", "response_invalid"}, {"no-newline", "read_failed"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			s, o, _ := fixture(t, tc.mode)
			r := Check(context.Background(), s, o)
			if r.Code != tc.code || r.Health != "unhealthy" || !r.Started || r.Invoked || r.Cleanup != "complete" {
				t.Fatalf("failure receipt: %+v", r)
			}
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "SECRET") {
				t.Fatal("native failure leaked")
			}
		})
	}
}

func TestTimeoutAndCancellationRemainBounded(t *testing.T) {
	for _, mode := range []string{"blocked", "no-read", "partial"} {
		t.Run(mode, func(t *testing.T) {
			s, o, _ := fixture(t, mode)
			o.Timeout = 80 * time.Millisecond
			start := time.Now()
			r := Check(context.Background(), s, o)
			if r.Health != "unhealthy" || r.Code != "cancelled_or_timeout" || r.Invoked || r.Cleanup != "complete" || time.Since(start) > 2*time.Second {
				t.Fatalf("deadline/cleanup: %+v", r)
			}
		})
	}
	s, o, dir := fixture(t, "healthy")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Check(ctx, s, o)
	if r.Started || r.Code != "cancelled" {
		t.Fatalf("cancelled launched: %+v", r)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("cancelled check wrote state")
	}
}

func TestOwnedGroupCleanupIncludesInheritedDescendant(t *testing.T) {
	s, o, dir := fixture(t, "descendant")
	r := Check(context.Background(), s, o)
	if r.Health != "healthy" || r.Cleanup != "complete" {
		t.Fatalf("cleanup: %+v", r)
	}
	b, err := os.ReadFile(filepath.Join(dir, "descendant-pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		// Read-only liveness check; never signal a PID saved in test output.
		out, e := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if e != nil || len(strings.TrimSpace(string(out))) == 0 || strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned inherited descendant still running")
}

func TestInvalidDeclarationMissingEnvironmentAndHTTPSNeverLaunch(t *testing.T) {
	s, o, dir := fixture(t, "healthy")
	for _, change := range []func(*library.ResolvedTool){
		func(s *library.ResolvedTool) { s.PackageDigest = "unpinned" },
		func(s *library.ResolvedTool) { s.Tool.Command = []string{"sh\x00"} },
		func(s *library.ResolvedTool) { s.Tool.EnvRefs = []string{"HOME"} },
		func(s *library.ResolvedTool) { s.Tool.EnvRefs = []string{"SELECTED_SECRET", "SELECTED_SECRET"} },
		func(s *library.ResolvedTool) { s.Tool.Transport = "unknown" },
	} {
		bad := s
		change(&bad)
		r := Check(context.Background(), bad, o)
		if r.Declared || r.Started || r.Code != "invalid_declaration" {
			t.Fatalf("bad declaration accepted: %+v", r)
		}
	}
	s.Tool.EnvRefs = []string{"MISSING_KEY"}
	r := Check(context.Background(), s, o)
	if r.Availability != "unavailable" || r.Code != "environment_missing" || r.Started {
		t.Fatalf("missing env: %+v", r)
	}
	s.Tool = library.Tool{ID: "remote", Transport: "https", URL: "https://127.0.0.1:1/mcp"}
	r = Check(context.Background(), s, o)
	if !r.Declared || r.Health != "unsupported" || r.Availability != "unknown" || r.Started || r.Code != "https_unqualified" {
		t.Fatalf("https fabricated health: %+v", r)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("non-executing checks wrote state")
	}
}

func TestStrictResponseJSON(t *testing.T) {
	for _, bad := range []string{`{"a":1,"a":2}`, `{"nested":{"x":true,"x":false}}`, `{} {}`, "{\"x\":\"\xff\"}", strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34)} {
		if strictJSON([]byte(bad)) {
			t.Fatalf("invalid or ambiguous JSON accepted: %q", bad)
		}
	}
	if !strictJSON([]byte(`{"x":[1,true,null,"a"],"y":{}}`)) {
		t.Fatal("valid JSON rejected")
	}
}

func TestReservedEnvironmentRejectedBeforeLookupOrLaunch(t *testing.T) {
	for _, key := range []string{"HOME", "TMPDIR", "TMP", "TEMP", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
		t.Run(key, func(t *testing.T) {
			s, o, dir := fixture(t, "healthy")
			s.Tool.EnvRefs = []string{key}
			o.LookupEnv = func(string) (string, bool) {
				t.Fatal("invalid environment reference reached lookup")
				return "HOSTILE_PRIVATE_PATH", true
			}
			r := Check(context.Background(), s, o)
			b, _ := json.Marshal(r)
			if r.Declared || r.Started || r.Code != "invalid_declaration" || strings.Contains(string(b), key) || strings.Contains(string(b), "HOSTILE") {
				t.Fatalf("reserved key disclosed or accepted: %s", b)
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 {
				t.Fatal("invalid declaration executed or wrote state")
			}
		})
	}
}

func TestOversizedEnvironmentDoesNotStart(t *testing.T) {
	s, o, _ := fixture(t, "healthy")
	o.LookupEnv = func(string) (string, bool) { return strings.Repeat("S", 65<<10), true }
	r := Check(context.Background(), s, o)
	if r.Started || r.Code != "environment_invalid" {
		t.Fatalf("oversized environment accepted: %+v", r)
	}
}

func TestCapabilityBooleanCannotBeNull(t *testing.T) {
	for _, caps := range []string{`{"tools":{"listChanged":null}}`, `{"tools":{},"resources":{"subscribe":null}}`, `{"tools":null}`} {
		var value map[string]json.RawMessage
		_ = json.Unmarshal([]byte(caps), &value)
		if validCapabilities(value) {
			t.Fatalf("ambiguous capability accepted: %s", caps)
		}
	}
}

func TestDocumentedVersionNegotiationAndPagination(t *testing.T) {
	for _, version := range []string{"2024-11-05", "2025-03-26", "2025-06-18", protocolVersion} {
		t.Run(version, func(t *testing.T) {
			s, o, _ := fixture(t, "version-"+version)
			r := Check(context.Background(), s, o)
			if r.Health != "healthy" || r.ProtocolVersion != version || r.ToolCount != 2 || r.Cleanup != "complete" || r.Invoked {
				t.Fatalf("negotiated version: %+v", r)
			}
		})
	}
}

// This owned subprocess is selected only by test argv and an explicit EnvRef.
// It intentionally emits private strings so the receipt's redaction is tested.
func TestMCPFixtureProcess(t *testing.T) {
	if os.Getenv("KEELMUX_MCP_FIXTURE") != "1" {
		return
	}
	i := 0
	for ; i < len(os.Args) && os.Args[i] != "--"; i++ {
	}
	mode := os.Args[i+1]
	if len(os.Args) > i+2 && os.Args[i+2] != "$literal `argv` ; unchanged" {
		os.Exit(30)
	}
	if mode == "grandchild" {
		signal.Ignore(syscall.SIGTERM)
		for {
			time.Sleep(time.Second)
		}
	}
	if os.Getenv("SELECTED_SECRET") != "SECRET_MUST_NOT_ESCAPE" || os.Getenv("AMBIENT_SECRET") != "" {
		os.Exit(31)
	}
	home := os.Getenv("HOME")
	tmp := os.Getenv("TMPDIR")
	root := filepath.Dir(home)
	if home == "" || filepath.Dir(tmp) != root || os.Getenv("XDG_CACHE_HOME") != filepath.Join(root, "cache") {
		os.Exit(32)
	}
	if st, e := os.Stat(root); e != nil || st.Mode().Perm() != 0700 {
		os.Exit(33)
	}
	_ = os.WriteFile("private-root", []byte(root), 0600)
	_ = os.WriteFile(filepath.Join(home, "scratch"), []byte("private child data"), 0600)
	fmt.Fprintln(os.Stderr, "SECRET_MUST_NOT_ESCAPE native error log")
	if mode == "descendant" {
		exe, _ := os.Executable()
		child := exec.Command(exe, "-test.run=^TestMCPFixtureProcess$", "--", "grandchild")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(34)
		}
		_ = os.WriteFile("descendant-pid", []byte(strconv.Itoa(child.Process.Pid)), 0600)
	}
	if mode == "no-read" {
		for {
			time.Sleep(time.Second)
		}
	}
	requests, _ := os.OpenFile("requests", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		fmt.Fprintln(requests, string(line))
		var msg struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(line, &msg)
		if msg.Method == "notifications/initialized" {
			continue
		}
		if msg.Method == "initialize" {
			switch mode {
			case "blocked":
				for {
					time.Sleep(time.Second)
				}
			case "partial":
				fmt.Print(`{"jsonrpc":`)
				for {
					time.Sleep(time.Second)
				}
			case "wrong-id":
				fmt.Println(`{"jsonrpc":"2.0","id":99,"result":{}}`)
				continue
			case "string-id":
				fmt.Println(`{"jsonrpc":"2.0","id":"1","result":{}}`)
				continue
			case "duplicate-id":
				fmt.Println(`{"jsonrpc":"2.0","id":99,"id":1,"result":{}}`)
				continue
			case "malformed":
				fmt.Println(`PRIVATE_SECRET not json`)
				continue
			case "oversized":
				fmt.Println(strings.Repeat("x", maxFrame+1))
				continue
			case "no-newline":
				fmt.Print(`{"jsonrpc":"2.0","id":1,"result":{}}`)
				os.Exit(0)
			case "server-error":
				fmt.Println(`{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"PRIVATE_SECRET"}}`)
				continue
			case "sampling", "elicitation", "roots":
				fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":5,\"method\":\"%s/request\",\"params\":{}}\n", mode)
				continue
			case "notifications":
				for j := 0; j < maxFrames+1; j++ {
					fmt.Println(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"PRIVATE_SECRET"}}`)
				}
				continue
			case "byte-limit":
				for j := 0; j < 5; j++ {
					fmt.Printf("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{\"data\":%q}}\n", strings.Repeat("x", 220<<10))
				}
				continue
			}
			version := protocolVersion
			if strings.HasPrefix(mode, "version-") {
				version = strings.TrimPrefix(mode, "version-")
			}
			if mode == "unknown-version" {
				version = "future"
			}
			caps := `{"tools":{}}`
			if mode == "unknown-policy" {
				caps = `{"tools":{},"sampling":{}}`
			}
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":%q,\"capabilities\":%s,\"serverInfo\":{\"name\":\"PRIVATE_SERVER\",\"version\":\"PRIVATE_VERSION\"},\"instructions\":\"PRIVATE_INSTRUCTIONS\"}}\n", version, caps)
		} else if msg.Method == "tools/list" {
			result := `{"tools":[{"name":"first","description":"PRIVATE_SECRET","inputSchema":{"type":"object"}},{"name":"second","inputSchema":{"type":"object"}}]}`
			if strings.HasPrefix(mode, "version-") {
				fmt.Println(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"PRIVATE_SECRET"}}`)
				if msg.ID == 2 {
					result = `{"tools":[{"name":"first","inputSchema":{"type":"object"}}],"nextCursor":"last-page"}`
				} else {
					var request struct {
						Params struct {
							Cursor string `json:"cursor"`
						} `json:"params"`
					}
					_ = json.Unmarshal(line, &request)
					if request.Params.Cursor != "last-page" {
						os.Exit(36)
					}
					result = `{"tools":[{"name":"second","inputSchema":{"type":"object"}}]}`
				}
			}
			switch mode {
			case "tool-limit":
				var entries []string
				for j := 0; j <= maxTools; j++ {
					entries = append(entries, fmt.Sprintf(`{"name":"tool%d","inputSchema":{"type":"object"}}`, j))
				}
				result = `{"tools":[` + strings.Join(entries, ",") + `]}`
			case "repeated-cursor":
				result = `{"tools":[],"nextCursor":"same"}`
			case "infinite-pages":
				result = fmt.Sprintf(`{"tools":[],"nextCursor":"page%d"}`, msg.ID)
			case "duplicate-tools":
				result = `{"tools":[{"name":"same","inputSchema":{"type":"object"}},{"name":"same","inputSchema":{"type":"object"}}]}`
			case "invalid-schema":
				result = `{"tools":[{"name":"bad","inputSchema":{"type":"array"}}]}`
			case "unknown-result":
				result = `{"tools":[],"execute":"SECRET"}`
			}
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":%s}\n", msg.ID, result)
		} else {
			fmt.Fprintln(requests, "FORBIDDEN METHOD")
			os.Exit(35)
		}
	}
	requests.Close()
	os.Exit(0)
}
