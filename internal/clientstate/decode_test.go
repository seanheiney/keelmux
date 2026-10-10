package clientstate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDecodeMatchesLoadWithoutChangingSnapshot(t *testing.T) {
	for _, version := range []int{1, Version} {
		s := New()
		s.Version = version
		raw, e := json.Marshal(s)
		if e != nil {
			t.Fatal(e)
		}
		original := append([]byte{}, raw...)
		decoded, e := Decode(raw)
		if e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(t.TempDir(), "place.json")
		os.WriteFile(path, raw, 0600)
		loaded, e := Load(path)
		if e != nil || !reflect.DeepEqual(decoded, loaded) || !bytes.Equal(raw, original) {
			t.Fatal("parser drift or changed snapshot", e)
		}
	}
	for _, raw := range [][]byte{[]byte(`{"version":2,"client_id":"a","client_id":"b"}`), []byte(`{"version":2,"Client_ID":"private"}`), bytes.Repeat([]byte("x"), (4<<20)+1), []byte("\xff")} {
		if _, e := Decode(raw); e == nil {
			t.Fatal("accepted invalid place snapshot")
		}
	}
}
