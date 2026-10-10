package channels

import "testing"

func TestCanonicalChannelPreservesOneExternalAddress(t *testing.T) {
	for _, tc := range []struct {
		provider  Provider
		raw, want string
	}{{IRC, "#Agents", "#agents"}, {IRC, "#AGENTS", "#agents"}, {IRC, "&Local.Path_/room-1", "&local.path_/room-1"}, {Mattermost, "ABCExactId", "ABCExactId"}, {Slack, "CExactID", "CExactID"}} {
		got, err := CanonicalChannel(tc.provider, tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("canonical address provider=%s got=%q err=%v", tc.provider, got, err)
		}
	}
	for _, raw := range []string{"#", "agents", "#a[b", "#a{b", "#a\\b", "#a|b", "#a^b", "#a~b", "#世", "#a b", "#a,b", "#a:b", "#a\r\n"} {
		if _, err := CanonicalChannel(IRC, raw); err == nil {
			t.Fatal("unnegotiated special-equivalent/invalid address accepted")
		}
	}
	upper, _ := CanonicalChannel(IRC, "#Agents")
	lower, _ := CanonicalChannel(IRC, "#agents")
	if upper != lower {
		t.Fatal("wire aliases retained distinct route addresses")
	}
}
