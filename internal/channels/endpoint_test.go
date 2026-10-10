package channels

import "testing"

func TestCanonicalEndpointPinsTLSScope(t *testing.T) {
	for _, tc := range []struct {
		provider  Provider
		raw, want string
	}{{Mattermost, "https://MM.EXAMPLE/base/", "https://mm.example/base"}, {Slack, "https://SLACK.COM/api/", "https://slack.com/api"}, {IRC, "ircs://IRC.EXAMPLE:6697/", "ircs://irc.example:6697"}, {IRC, "ircs://[::1]:6697", "ircs://[::1]:6697"}} {
		got, err := CanonicalEndpoint(tc.provider, tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("canonical endpoint got=%q err=%v", got, err)
		}
	}
	for _, tc := range []struct {
		provider Provider
		raw      string
	}{{Mattermost, "http://mm.example"}, {Mattermost, "https://user:password@mm.example"}, {Mattermost, "https://mm.example?"}, {Mattermost, "https://mm.example?x=1"}, {Mattermost, "https://mm.example/../other"}, {Mattermost, "https://mm.example:99999"}, {Slack, "https://proxy.example/api"}, {Slack, "https://slack.com/api/other"}, {IRC, "irc://irc.example:6667"}, {IRC, "ircs://irc.example"}, {IRC, "ircs://irc.example:6697/path"}, {IRC, "ircs://irc.example:06697"}, {Provider("unknown"), "https://example.test"}} {
		if _, err := CanonicalEndpoint(tc.provider, tc.raw); err == nil {
			t.Fatalf("unsupported scope accepted provider=%s", tc.provider)
		}
	}
}
