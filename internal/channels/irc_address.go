package channels

import (
	"encoding/base32"
	"encoding/hex"
	"strings"

	"github.com/seanheiney/keelmux/internal/domain"
)

// IRCThreadNick preserves all 128 bits of the stable thread identity within
// Ergo's 32-character services-account bound. Display names grant no authority.
func IRCThreadNick(thread domain.ID) (string, error) {
	if !domain.ValidObjectID(thread, "thread") {
		return "", ErrInvalid
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(string(thread), "thread_"))
	if err != nil {
		return "", ErrInvalid
	}
	return "kt-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)), nil
}
