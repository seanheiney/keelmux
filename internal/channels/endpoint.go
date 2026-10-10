package channels

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// CanonicalEndpoint normalizes benign host case and trailing slashes before
// freezing a binding. It never weakens TLS, changes ports or changes realms.
// Adapters subsequently require this exact canonical value in every proof.
func CanonicalEndpoint(provider Provider, raw string) (string, error) {
	errScope := errors.New("channel endpoint is not a supported TLS scope")
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 1024 || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.Contains(u.Path, "..") {
		return "", errScope
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", errScope
		}
	}
	canonical := u.String()
	switch provider {
	case Mattermost:
		if u.Scheme != "https" {
			return "", errScope
		}
	case Slack:
		if canonical != "https://slack.com/api" {
			return "", errScope
		}
	case IRC:
		if u.Scheme != "ircs" || u.Path != "" {
			return "", errScope
		}
		host, port, err := net.SplitHostPort(u.Host)
		if err != nil || host == "" || port == "" || net.JoinHostPort(host, port) != u.Host {
			return "", errScope
		}
	default:
		return "", errScope
	}
	return canonical, nil
}
