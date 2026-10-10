package nativepty

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

const testBootUUID = "0e1a6aba-8ee6-4689-be26-ed53b8ca3123"

func TestKernelBootUUIDValidation(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, uuid := range []string{testBootUUID, strings.ToUpper(testBootUUID)} {
			got, err := validatedBootID(platform, uuid)
			if err != nil || got != platform+":"+testBootUUID {
				t.Fatalf("valid %s UUID normalization: %q %v", platform, got, err)
			}
		}
		for _, uuid := range []string{"", "1791497239:636714", strings.ReplaceAll(testBootUUID, "-", ""), testBootUUID + "\n", " " + testBootUUID, testBootUUID[:35] + "g", "0e1a6aba_8ee6-4689-be26-ed53b8ca3123", "00000000-0000-0000-0000-000000000000", testBootUUID[:35] + "\x00"} {
			got, err := validatedBootID(platform, uuid)
			if got != "" || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("untrusted %s kernel value %q accepted: %q %v", platform, uuid, got, err)
			}
		}
	}
	if got, err := validatedBootID("unknown", testBootUUID); got != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unknown platform accepted: %q %v", got, err)
	}
}

func TestStableBootComparisonRequiresCompatibleUUIDs(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		current := platform + ":" + testBootUUID
		for _, recorded := range []string{current, platform + ":" + strings.ToUpper(testBootUUID)} {
			if same, err := sameBoot(current, recorded); !same || err != nil {
				t.Fatalf("same stable boot classified differently: %v %v", same, err)
			}
		}
		other := platform + ":1e1a6aba-8ee6-4689-be26-ed53b8ca3123"
		if same, err := sameBoot(current, other); same || err != nil {
			t.Fatalf("known stable reboot not established: %v %v", same, err)
		}
		for _, recorded := range []string{"darwin:1791497239:636714", "darwin:1791497239:730048", "linux:", platform + ":00000000-0000-0000-0000-000000000000", "other:" + testBootUUID, testBootUUID} {
			if same, err := sameBoot(current, recorded); same || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unknown receipt established reboot or same boot: %q %v %v", recorded, same, err)
			}
		}
	}
	if same, err := sameBoot("darwin:"+testBootUUID, "linux:"+testBootUUID); same || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("cross-platform comparison established boot: %v %v", same, err)
	}
	if same, err := sameBoot("darwin:1791497239:730048", "darwin:1791497239:636714"); same || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("two mutable timestamps established reboot: %v %v", same, err)
	}
}

func TestUntrustedBootCannotEstablishDeathEvenForTerminalReceipt(t *testing.T) {
	b := testBackend(t, 0)
	boot, err := bootID()
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"intent", "running", "exited", "failed", "stopped"} {
		for _, badBoot := range []string{"darwin:1791497239:636714", "darwin:1791497239:730048", "linux:", "darwin:bad"} {
			t.Run(stage+"/"+badBoot, func(t *testing.T) {
				spec := testSpec("exit 99")
				r := receipt{Version: 1, SessionID: spec.Session.ID, HostID: spec.Session.HostID, Digest: launchDigest(spec, "process"), Auth: nonce(), LinkToken: nonce(), Mode: "process", Stage: stage, Ref: domain.RuntimeRef{Backend: "native", Socket: b.socket(spec.Session.Execution.ID), Session: nonce(), BootID: badBoot, ExecutionID: spec.Session.Execution.ID, OperationID: spec.OperationID}}
				if err := b.writeReceipt(r); err != nil {
					t.Fatal(err)
				}
				o, err := b.Inspect(context.Background(), r.Ref)
				if !errors.Is(err, ErrUnavailable) || o.Alive || o.Ref != r.Ref || !strings.Contains(o.Detail, "uncertain") {
					t.Fatalf("untrusted boot established death: %+v %v", o, err)
				}
				if err := b.Stop(context.Background(), r.Ref); !errors.Is(err, ErrUnavailable) {
					t.Fatalf("untrusted boot authorized stop acknowledgement: %v", err)
				}
				if _, err := b.LaunchProcess(context.Background(), spec); !errors.Is(err, ErrUnavailable) {
					t.Fatalf("untrusted boot authorized launch replay: %v", err)
				}
				after, err := b.readReceipt(r.Ref.ExecutionID)
				if err != nil || after.Ref != r.Ref || after.Stage != stage {
					t.Fatalf("untrusted receipt was rewritten: %+v %v", after.Ref, err)
				}
			})
		}
	}
	platform, _, _ := strings.Cut(boot, ":")
	other := platform + ":1e1a6aba-8ee6-4689-be26-ed53b8ca3123"
	if other == boot {
		other = platform + ":2e1a6aba-8ee6-4689-be26-ed53b8ca3123"
	}
	r := receipt{Stage: "running", Ref: domain.RuntimeRef{BootID: other}}
	o, err := b.observe(context.Background(), r)
	if err != nil || o.Alive || !strings.Contains(o.Detail, "old execution cannot survive") {
		t.Fatalf("known stable reboot lost death evidence: %+v %v", o, err)
	}
}

func TestLegacyBootReceiptCannotStopOwnedProcessOrReplay(t *testing.T) {
	b := testBackend(t, 0)
	marker := filepath.Join(b.state, "effects")
	spec := testSpec(`printf x >> "$1"; exec sleep 30`)
	spec.Argv = append(spec.Argv, "test", marker)
	ref := launchTest(t, b, spec, true)
	original, err := b.readReceipt(ref.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	// Only this test's fabricated receipt is restored, so cleanup can use the
	// original authenticated exact reference. Production never migrates receipts.
	t.Cleanup(func() {
		if err := b.writeReceipt(original); err != nil {
			t.Error(err)
		}
	})
	legacy := original
	legacy.Ref.BootID = "darwin:1791497239:636714"
	if err := b.writeReceipt(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Inspect(context.Background(), legacy.Ref); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("legacy live receipt established death: %v", err)
	}
	if _, err := b.connect(context.Background(), legacy); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("legacy receipt authorized socket connection: %v", err)
	}
	if err := b.Stop(context.Background(), legacy.Ref); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("legacy receipt authorized cleanup: %v", err)
	}
	if _, err := b.LaunchProcess(context.Background(), spec); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("legacy receipt authorized replay: %v", err)
	}
	if err := b.ValidateLink(context.Background(), original.SessionID, ref.ExecutionID, ref.OperationID, original.LinkToken); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("legacy receipt granted link authority: %v", err)
	}
	// Inspection through the original in-memory authenticated reference proves
	// rejected legacy cleanup did not signal the live child or lose its helper.
	o, err := b.observe(context.Background(), original)
	if err != nil || !o.Alive || o.Ref != ref {
		t.Fatalf("rejected cleanup affected original execution: %+v %v", o, err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "x" {
		t.Fatalf("launch effects replayed: %q %v", data, err)
	}
	after, err := b.readReceipt(ref.ExecutionID)
	if err != nil || after.Ref != legacy.Ref || after.Stage != legacy.Stage {
		t.Fatalf("legacy receipt was migrated: %+v %v", after.Ref, err)
	}
}
