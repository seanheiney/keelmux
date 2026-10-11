package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/peerbridge"
	"golang.org/x/sys/unix"
)

const peerPolicyHelp = `Usage: keelmux peer-policy COMMAND

provision --file PRIVATE_POLICY_JSON AUTHORITY
update --file PRIVATE_POLICY_JSON --expect-revision N AUTHORITY
inspect AUTHORITY

AUTHORITY is an explicit home-owned policy and recovery journal in an existing
canonical owned mode-0700 directory. Provision never overwrites existing state.
Update preserves its scope, host, principal, socket and all operation outcomes.
The policy grants only explicit inventory and terminal_read session actions.
This owner-managed SSH bridge has no writer, launch, stop or model action.
`

func peerPolicyCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprint(out, peerPolicyHelp)
		return err
	}
	command := args[0]
	fs := flag.NewFlagSet("peer-policy "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	file := fs.String("file", "", "owned mode-0600 desired policy JSON")
	expected := fs.Uint64("expect-revision", 0, "exact current authority revision")
	if err := fs.Parse(args[1:]); err != nil {
		return errors.New("invalid peer-policy arguments; use peer-policy --help")
	}
	if len(fs.Args()) != 1 {
		return errors.New("peer-policy needs one explicit authority path")
	}
	authority := fs.Args()[0]
	switch command {
	case "inspect":
		if fs.NFlag() != 0 {
			return errors.New("peer-policy inspect accepts no options")
		}
		policy, err := peerbridge.ReadPolicyFile(authority)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(policy)
	case "provision", "update":
		if *file == "" || command == "provision" && fs.NFlag() != 1 || command == "update" && (fs.NFlag() != 2 || *expected == 0) {
			return errors.New("use explicit --file and, for update, --expect-revision")
		}
		var policy peerbridge.ReadPolicy
		if err := config.ReadPrivateJSON(*file, 65536, &policy); err != nil {
			return err
		}
		var err error
		if command == "provision" {
			err = peerbridge.ProvisionPolicy(ctx, authority, policy)
		} else {
			err = peerbridge.UpdatePolicy(ctx, authority, *expected, policy)
		}
		if err != nil {
			return err
		}
		// The read-back exposes policy only, never retained private lease records.
		actual, err := peerbridge.ReadPolicyFile(authority)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(actual)
	default:
		return errors.New("unknown peer-policy command; use peer-policy --help")
	}
}

func servePeerBridge(args []string) error {
	if len(args) != 1 {
		return errors.New("peer-read-bridge needs one explicit home-owned authority path")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	in, err := bridgeStdio(os.Stdin, true)
	if err != nil {
		return err
	}
	out, err := bridgeStdio(os.Stdout, false)
	if err != nil {
		_ = in.Close()
		return err
	}
	return peerbridge.Serve(ctx, args[0], in, out)
}

// Inherited blocking stdio is not registered with Go's poller. Closing such a
// file cannot interrupt a quiet in-flight read, which would keep the SSH
// exporter alive after policy revocation. This exclusive command owns the
// stream: duplicate its descriptor and set nonblocking BEFORE wrapping it, so
// Close interrupts both reads and stalled writes. O_NONBLOCK is shared with
// the original descriptor; do not use this adapter for general CLI output.
func bridgeStdio(original *os.File, read bool) (*os.File, error) {
	refuse := errors.New("peer-read-bridge requires interruptible SSH stream pipes")
	if original == nil {
		return nil, refuse
	}
	raw, err := original.SyscallConn()
	if err != nil {
		return nil, refuse
	}
	fd := -1
	var setupErr error
	err = raw.Control(func(source uintptr) {
		var info unix.Stat_t
		setupErr = unix.Fstat(int(source), &info)
		if setupErr != nil {
			return
		}
		switch info.Mode & unix.S_IFMT {
		case unix.S_IFIFO:
		case unix.S_IFSOCK:
			var kind int
			kind, setupErr = unix.GetsockoptInt(int(source), unix.SOL_SOCKET, unix.SO_TYPE)
			if setupErr != nil || kind != unix.SOCK_STREAM {
				setupErr = refuse
				return
			}
		default:
			setupErr = refuse
			return
		}
		var flags int
		flags, setupErr = unix.FcntlInt(source, unix.F_GETFL, 0)
		if setupErr != nil {
			return
		}
		access := flags & unix.O_ACCMODE
		if read && access == unix.O_WRONLY || !read && access == unix.O_RDONLY {
			setupErr = refuse
			return
		}
		fd, setupErr = unix.FcntlInt(source, unix.F_DUPFD_CLOEXEC, 0)
		if setupErr == nil {
			setupErr = unix.SetNonblock(fd, true)
		}
	})
	if err != nil || setupErr != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return nil, refuse
	}
	file := os.NewFile(uintptr(fd), "peer-read-bridge-stream")
	if file == nil {
		_ = unix.Close(fd)
		return nil, refuse
	}
	if read {
		err = file.SetReadDeadline(time.Time{})
	} else {
		err = file.SetWriteDeadline(time.Time{})
	}
	if err != nil {
		_ = file.Close()
		return nil, refuse
	}
	return file, nil
}
