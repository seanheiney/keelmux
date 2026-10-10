package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalVerdictIncludesErrorsRecordedDuringReaderCleanup(t *testing.T) {
	trace := &tracedPolicy{resumeAcks: 2}
	if err := trace.finalError(); err != nil {
		t.Fatal(err)
	}
	trace.outputErrors++ // Recorded after the provisional h.run success.
	if trace.finalError() == nil {
		t.Fatal("late source projection failure did not invalidate final success")
	}
	trace.outputErrors = 0
	trace.sourceErrors++ // A lost source can finish without another policy call.
	if trace.finalError() == nil {
		t.Fatal("late source loss did not invalidate final success")
	}
}

func TestPublicErrorsNeverExposeOpaquePeerReasonsOrPaths(t *testing.T) {
	for _, value := range []string{"peer close reason private-token", "dial unix /private/tmp/private-path: denied"} {
		if got := publicErrorKind(errors.New(value)); got != "owned_runtime_failure" {
			t.Fatalf("opaque error got nonfinite public tag %q", got)
		}
	}
}

// A failed startup peer proof must not turn observed same-user paths into
// cleanup authority. Test actual socket inodes without spawning native work.
func TestSocketCleanupRequiresOwnedPeerAndOriginalInode(t *testing.T) {
	for _, tc := range []struct {
		name           string
		owned, replace bool
		removed        bool
	}{
		{name: "unproved_peer_keeps_socket"},
		{name: "proved_peer_removes_original", owned: true, removed: true},
		{name: "proved_peer_keeps_replacement", owned: true, replace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Darwin's Unix socket path limit is shorter than testing.T.TempDir
			// under the default long per-user temporary directory.
			dir, err := os.MkdirTemp("/tmp", "bp-peer-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "native.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			listener.SetUnlinkOnClose(false)
			defer listener.Close()
			original, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.replace {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan struct{})
			close(done) // The cleanup boundary is after the owned child exited.
			p := &process{done: done, socket: path, inode: original, socketOwned: tc.owned}
			p.stop()
			_, err = os.Lstat(path)
			if (tc.removed && !os.IsNotExist(err)) || (!tc.removed && err != nil) {
				t.Fatalf("removed=%t, want=%t (stat error %v)", os.IsNotExist(err), tc.removed, err)
			}
		})
	}
}
