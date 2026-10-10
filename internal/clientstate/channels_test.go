package clientstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestV1MigrationPreservesNativePlaceGroupsDraftAndLease(t *testing.T) {
	s := New()
	s.Version = 1
	g, _ := s.AddGroup("Product", "")
	s.SwitchGroup(g)
	s.Places[g] = Place{SessionID: "session", ThreadID: "thread", Filter: "query", Scroll: 2}
	s.SetDraft(Draft{ThreadID: "thread", RecipientID: "lane", Text: "native draft"})
	s.Pending = &Attachment{SessionID: "session", ExecutionID: "execution", Generation: 2, OperationID: "operation", ClientID: s.ClientID, Token: "owned-test-lease"}
	b, _ := json.Marshal(s)
	path := filepath.Join(t.TempDir(), "place.json")
	os.WriteFile(path, b, 0600)
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Version = Version
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("migration changed original fields: %+v", got)
	}
	if err := Save(path, got); err != nil {
		t.Fatal(err)
	}
	// The published old schema's explicit version gate refuses this persisted
	// file. It cannot load, mutate and overwrite the newly addressed state.
	var old struct {
		Version int `json:"version"`
	}
	after, _ := os.ReadFile(path)
	json.Unmarshal(after, &old)
	if old.Version == 1 {
		t.Fatal("old version gate could accept migrated authority")
	}
}

func TestChannelDraftsAreAddressedAndPendingCannotBeEdited(t *testing.T) {
	s := New()
	s.SetDraft(Draft{ThreadID: "thread", RecipientID: "binding", Text: "native"})
	for _, d := range []ChannelDraft{{"thread", "binding", "reply"}, {"thread", "other", "other binding"}, {"other", "binding", "other thread"}} {
		if err := s.SetChannelDraft(d); err != nil {
			t.Fatal(err)
		}
	}
	s.PendingChannel = &ChannelIntent{"message", "binding", "thread", "reply"}
	if err := s.SetChannelDraft(ChannelDraft{"thread", "binding", "edited"}); err == nil {
		t.Fatal("pending content mutated")
	}
	if err := s.SetChannelDraft(ChannelDraft{"thread", "other", "new text"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "place.json")
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("restart lost addressed intent: %v %+v", err, got)
	}
	if d, _ := got.Draft("thread", "binding"); d.Text != "native" {
		t.Fatal("channel draft overwrote native lane draft")
	}
}

func TestInvalidChannelAuthorityPreservesPriorFile(t *testing.T) {
	s := New()
	s.SetChannelDraft(ChannelDraft{"thread", "binding", "frozen"})
	s.PendingChannel = &ChannelIntent{"message", "binding", "thread", "frozen"}
	path := filepath.Join(t.TempDir(), "place.json")
	Save(path, s)
	before, _ := os.ReadFile(path)
	for _, change := range []func(*State){
		func(s *State) { s.PendingChannel.ThreadID = "other" },
		func(s *State) { s.PendingChannel.Text = "changed" },
		func(s *State) { s.PendingChannel.MessageID = "bad\nID" },
		func(s *State) { s.ChannelDrafts = append(s.ChannelDrafts, s.ChannelDrafts[0]) },
	} {
		copyState, _ := Load(path)
		change(&copyState)
		if err := Save(path, copyState); err == nil {
			t.Fatal("invalid intent saved")
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(before) {
			t.Fatal("failed save erased original authority")
		}
	}
}

func TestNewUnknownAndAmbiguousV1FieldsRefuseWithoutReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "place.json")
	for _, raw := range []string{
		`{"version":2,"client_id":"client","future_authority":{"token":"test"}}`,
		`{"version":2,"client_id":"client","version":1}`,
		`{"version":2,"client_id":"client","channel_drafts":[{"thread_id":"t","binding_id":"b","text":"frozen"}],"pending_channel_send":{"message_id":"m","thread_id":"t","binding_id":"b","text":"frozen"},"pending_channel_send":null}`,
		`{"version":2,"client_id":"client","channel_drafts":[{"thread_id":"t","binding_id":"b","text":"frozen"}],"pending_channel_send":{"message_id":"m","thread_id":"t","binding_id":"b","text":"frozen"},"PENDING_CHANNEL_SEND":null}`,
		`{"version":2,"client_id":"client","channel_drafts":[{"thread_id":"t","binding_id":"b","text":"frozen"}],"pending_channel_send":{"message_id":"m","thread_id":"t","binding_id":"b","text":"frozen"},"pending_channel_\u017fend":null}`,
		`{"version":2,"client_id":"client","VERSION":1}`,
		`{"version":2,"client_id":"client","places":{},"CHANNEL_DRAFTS":[]}`,
		`{"version":1,"client_id":"client","channel_drafts":null}`,
		`{"version":1,"client_id":"client","channel_drafts":[]}`,
		`{"version":1,"client_id":"client","pending_channel_send":null}`,
		`{"version":1,"client_id":"client","channel_drafts":[{"thread_id":"t","binding_id":"b","text":"text"}]}`,
		`{"version":2,"client_id":"client","pending_channel_send":{"message_id":"m","thread_id":"t","binding_id":"b","text":"frozen"}}`,
	} {
		os.WriteFile(path, []byte(raw), 0600)
		if _, err := Load(path); err == nil {
			t.Fatal("ambiguous authority accepted")
		}
		after, _ := os.ReadFile(path)
		if string(after) != raw {
			t.Fatal("load reset invalid authority")
		}
	}
}

func TestChannelPlaceFiniteUTF8AndOversizeReaderPreservesBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "place.json")
	valid := `{"version":2,"client_id":"client","places":{}}`
	for _, raw := range []string{
		valid + strings.Repeat(" ", (4<<20)-len(valid)) + `{"pending_channel_send":null}`,
		`{"version":2,"client_id":"client","places":{},"channel_drafts":[{"thread_id":"t","binding_id":"b","text":"` + string([]byte{0xff}) + `"}]}`,
	} {
		os.WriteFile(path, []byte(raw), 0600)
		if _, err := Load(path); err == nil {
			t.Fatal("oversize/invalid UTF8 accepted as authoritative state")
		}
		after, _ := os.ReadFile(path)
		if string(after) != raw {
			t.Fatal("refusal modified authority bytes")
		}
	}
}

func TestChannelPlaceAndLockRejectUnsafeFilesWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "directory", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "place.json")
			bytes := []byte(`{"version":2,"client_id":"client","places":{}}`)
			create := func(path string) {
				switch kind {
				case "symlink":
					os.WriteFile(path+"-target", bytes, 0600)
					if err := os.Symlink(path+"-target", path); err != nil {
						t.Fatal(err)
					}
				case "fifo":
					if err := unix.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				case "directory":
					os.Mkdir(path, 0700)
				case "permissions":
					os.WriteFile(path, bytes, 0644)
					os.Chmod(path, 0644)
				}
			}
			create(path)
			create(path + ".lock")
			start := time.Now()
			if _, err := Load(path); err == nil {
				t.Fatal("unsafe state descriptor accepted")
			}
			if lock, err := AcquireLock(path); err == nil || lock != nil {
				t.Fatal("unsafe lock descriptor accepted")
			}
			if time.Since(start) > time.Second {
				t.Fatal("special state/lock blocked caller")
			}
			if kind == "symlink" {
				after, _ := os.ReadFile(path + "-target")
				if string(after) != string(bytes) {
					t.Fatal("symlink target modified")
				}
			} else if kind == "permissions" {
				info, _ := os.Stat(path)
				lockInfo, _ := os.Stat(path + ".lock")
				if info.Mode().Perm() != 0644 || lockInfo.Mode().Perm() != 0644 {
					t.Fatal("refusal silently adopted unsafe file")
				}
			}
		})
	}
}

func TestPlaceMapIdentityRemainsCaseSensitive(t *testing.T) {
	s := New()
	s.Places["GroupA"] = Place{SessionID: "first"}
	s.Places["groupa"] = Place{SessionID: "second"}
	path := filepath.Join(t.TempDir(), "place.json")
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(s, got) {
		t.Fatalf("schema case refusal folded stable map identities: %v", err)
	}
}
