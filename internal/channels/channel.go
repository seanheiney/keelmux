package channels

import (
	"errors"
	"strings"
)

// CanonicalChannel freezes a single external address before route comparisons.
// IRC supports an ASCII subset with the same case equivalence under ascii,
// rfc1459 and strict-rfc1459. Special-equivalent characters and Unicode require
// a persisted negotiated case contract and are deliberately unsupported here.
func CanonicalChannel(provider Provider, raw string) (string, error) {
	errScope := errors.New("channel address is not a supported canonical scope")
	if !ValidID(raw) {
		return "", errScope
	}
	switch provider {
	case Mattermost, Slack:
		return raw, nil
	case IRC:
		if len(raw) < 2 || len(raw) > 200 || (raw[0] != '#' && raw[0] != '&') {
			return "", errScope
		}
		for _, r := range raw[1:] {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./", r)) {
				return "", errScope
			}
		}
		return strings.ToLower(raw), nil
	default:
		return "", errScope
	}
}
