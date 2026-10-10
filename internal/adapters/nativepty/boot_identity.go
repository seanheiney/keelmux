package nativepty

import (
	"fmt"
	"strings"
)

// Stable boot identities are kernel boot-session UUIDs. Wall-clock timestamps
// (including the former Darwin kern.boottime receipt) cannot prove a reboot.
func validatedBootID(platform, uuid string) (string, error) {
	if platform != "darwin" && platform != "linux" {
		return "", fmt.Errorf("%w: unsupported boot identity platform", ErrUnavailable)
	}
	if len(uuid) != 36 {
		return "", fmt.Errorf("%w: invalid kernel boot UUID", ErrUnavailable)
	}
	nonzero := false
	for i := range len(uuid) {
		c := uuid[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return "", fmt.Errorf("%w: invalid kernel boot UUID", ErrUnavailable)
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return "", fmt.Errorf("%w: invalid kernel boot UUID", ErrUnavailable)
		}
		nonzero = nonzero || c != '0'
	}
	if !nonzero {
		return "", fmt.Errorf("%w: empty kernel boot UUID", ErrUnavailable)
	}
	return platform + ":" + strings.ToLower(uuid), nil
}

// A mismatch proves death only between two validated stable identities from
// the same platform. Unknown formats remain uncertain even if strings differ.
func sameBoot(current, recorded string) (bool, error) {
	platform, uuid, ok := strings.Cut(current, ":")
	if !ok {
		return false, ErrUnavailable
	}
	current, err := validatedBootID(platform, uuid)
	if err != nil {
		return false, err
	}
	recordedPlatform, uuid, ok := strings.Cut(recorded, ":")
	if !ok || recordedPlatform != platform {
		return false, fmt.Errorf("%w: incompatible recorded boot identity", ErrUnavailable)
	}
	recorded, err = validatedBootID(recordedPlatform, uuid)
	if err != nil {
		return false, err
	}
	return current == recorded, nil
}
