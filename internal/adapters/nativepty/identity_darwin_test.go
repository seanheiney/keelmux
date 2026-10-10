package nativepty

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinBootIdentityReadsOnlyStableKernelUUID(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		readErr     error
		unavailable bool
	}{
		{"valid", "0E1A6ABA-8EE6-4689-BE26-ED53B8CA3123", nil, false},
		{"unsupported sysctl", "", unix.ENOENT, true},
		{"permission denied", "", unix.EPERM, true},
		{"malformed", "1791497239:636714", nil, true},
		{"empty", "", nil, true},
		{"nil UUID", "00000000-0000-0000-0000-000000000000", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, err := darwinBootID(func(key string) (string, error) {
				calls++
				if key != "kern.bootsessionuuid" {
					t.Fatalf("read mutable or fallback boot identity %q", key)
				}
				return tc.value, tc.readErr
			})
			if calls != 1 {
				t.Fatalf("attempted fallback after stable boot read: %d calls", calls)
			}
			if tc.unavailable {
				if got != "" || !errors.Is(err, ErrUnavailable) {
					t.Fatalf("kernel UUID failure accepted: %q %v", got, err)
				}
			} else if got != "darwin:"+testBootUUID || err != nil {
				t.Fatalf("kernel UUID identity mismatch: %q %v", got, err)
			}
		})
	}
}

func TestDarwinLiveKernelBootSessionUUID(t *testing.T) {
	raw, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		t.Fatal(err)
	}
	want, err := validatedBootID("darwin", raw)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		got, err := bootID()
		if err != nil || got != want {
			t.Fatalf("live kernel boot UUID mismatch: %q %q %v", got, want, err)
		}
	}
	t.Logf("kern.bootsessionuuid=%s; canonical boot_id=%s", raw, want)
}
