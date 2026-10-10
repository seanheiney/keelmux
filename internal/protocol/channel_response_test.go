package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
)

func responseReceipt() channels.Delivery {
	return channels.Delivery{ID: "message", MessageID: "message", BindingID: "binding", State: channels.Delivered, PayloadHash: [32]byte{1, 2, 3}, RemoteID: "remote-exact", UpdatedAt: time.Unix(1700000000, 0).UTC()}
}

func TestChannelResponseRefusesAmbiguousReceiptBeforePublication(t *testing.T) {
	d := responseReceipt()
	raw, _ := json.Marshal(d)
	good := string(raw)
	cases := map[string]string{
		"trailing-object":      good + ` {"State":"uncertain"}`,
		"trailing-scalar":      good + ` true`,
		"duplicate":            strings.Replace(good, `"State":"delivered"`, `"State":"uncertain","State":"delivered"`, 1),
		"case-alias":           strings.Replace(good, `"State":"delivered"`, `"State":"uncertain","state":"delivered"`, 1),
		"unicode-schema-alias": strings.Replace(good, `"State":"delivered"`, `"ſtate":"delivered"`, 1),
		"unknown":              good[:len(good)-1] + `,"private_diagnostic":"refused"}`,
		"invalid-utf8":         strings.Replace(good, `"RemoteID":"remote-exact"`, `"RemoteID":"remote-`+string([]byte{255})+`"`, 1),
		"array-short":          strings.Replace(good, `"PayloadHash":[1,2,3,`, `"PayloadHash":[1,2,`, 1),
		"array-long":           strings.Replace(good, `"PayloadHash":[1,2,3,`, `"PayloadHash":[1,2,3,4,`, 1),
		"wrong-type":           strings.Replace(good, `"State":"delivered"`, `"State":true`, 1),
		"null-struct":          "null",
		"oversized-identity":   strings.Replace(good, "remote-exact", strings.Repeat("x", 1025), 1),
		"byte-overflow":        strings.Replace(good, `"PayloadHash":[1,2,3,`, `"PayloadHash":[256,2,3,`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			before := channels.Delivery{State: channels.Uncertain, RemoteID: "retained"}
			out := before
			if !errors.Is(decodeChannelResponse([]byte(body), &out), errChannelResponse) {
				t.Fatal("ambiguous receipt accepted")
			}
			if out != before {
				t.Fatal("malformed response partially published a receipt")
			}
		})
	}
	var out channels.Delivery
	if err := decodeChannelResponse(raw, &out); err != nil || out != d {
		t.Fatal("exact uppercase DTO refused", err)
	}
}

func TestChannelResponseUsesExactTaggedSchemaAndDynamicMapIdentity(t *testing.T) {
	var status ChannelStatus
	for _, body := range []string{`{"Binding_id":"binding"}`, `{"binding_id":"a","binding_id":"b"}`, `{"ſemantic_dispatch":false}`, `{"semantic_dispatch":false,"Semantic_dispatch":true}`} {
		if decodeChannelResponse([]byte(body), &status) == nil {
			t.Fatal("tag alias/duplicate accepted", body)
		}
	}
	type dynamic struct {
		Places map[string]struct {
			ID string `json:"id"`
		} `json:"places"`
	}
	var out dynamic
	if err := decodeChannelResponse([]byte(`{"places":{"GroupA":{"id":"A"},"groupa":{"id":"a"}}}`), &out); err != nil || len(out.Places) != 2 {
		t.Fatal("case-sensitive dynamic IDs conflated", err)
	}
	for _, body := range []string{`{"places":{"A":{"id":"one"},"A":{"id":"two"}}}`, `{"places":{"A":{"ID":"aliased"}}}`} {
		if decodeChannelResponse([]byte(body), &out) == nil {
			t.Fatal("dynamic duplicate/schema alias accepted")
		}
	}
}

func TestChannelResponseBoundsCollectionsDepthNodesAndValidEscapedText(t *testing.T) {
	var history ChannelHistory
	messages := make([]channels.Message, channels.MaxPageSize)
	for i := range messages {
		messages[i] = channels.Message{ID: domain.ID(fmt.Sprint(i)), Text: strings.Repeat("<", channels.MaxTextBytes)}
	}
	raw, _ := json.Marshal(ChannelHistory{ThreadID: "thread", Messages: messages, SnapshotOnly: true})
	if len(raw) < 9<<20 {
		t.Fatal("maximum escaped-text control did not exercise wire size")
	}
	if err := decodeChannelResponse(raw, &history); err != nil || len(history.Messages) != 100 {
		t.Fatal("valid maximum escaped history refused", err)
	}
	messages = append(messages, channels.Message{ID: "extra", Text: "extra"})
	raw, _ = json.Marshal(ChannelHistory{Messages: messages})
	if decodeChannelResponse(raw, &history) == nil {
		t.Fatal("oversized authored page accepted")
	}
	raw, _ = json.Marshal(ChannelHistory{Messages: []channels.Message{{Text: strings.Repeat("x", channels.MaxTextBytes+1)}}})
	if decodeChannelResponse(raw, &history) == nil {
		t.Fatal("oversized authored text accepted")
	}
	var statuses []ChannelStatus
	raw, _ = json.Marshal(make([]ChannelStatus, 129))
	if decodeChannelResponse(raw, &statuses) == nil {
		t.Fatal("oversized binding list accepted")
	}
	raw, _ = json.Marshal(ChannelStatus{Allowlist: make([]string, 257)})
	var status ChannelStatus
	if decodeChannelResponse(raw, &status) == nil {
		t.Fatal("oversized allowlist accepted")
	}
	type chain struct {
		Next *chain `json:"next"`
	}
	nested := &chain{}
	cur := nested
	for range 33 {
		cur.Next = &chain{}
		cur = cur.Next
	}
	raw, _ = json.Marshal(nested)
	var c chain
	if decodeChannelResponse(raw, &c) == nil {
		t.Fatal("excessive schema nesting accepted")
	}
	type wide struct {
		Values [320][320]string `json:"values"`
	}
	raw, _ = json.Marshal(wide{})
	var w wide
	if decodeChannelResponse(raw, &w) == nil {
		t.Fatal("excessive JSON nodes accepted")
	}
	if decodeChannelResponse([]byte(strings.Repeat(" ", maxChannelResponseBytes+1)), &history) == nil {
		t.Fatal("unbounded document accepted")
	}
}

func TestCallChannelKeepsHTTPFailuresBoundedAndNeverFollowsRedirects(t *testing.T) {
	var effects atomic.Int32
	d := responseReceipt()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good":
			json.NewEncoder(w).Encode(d)
		case "/missing":
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(Error{Message: "channel receipt not found"})
		case "/bad-missing":
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":"missing"}{"error":"different"}`)
		case "/large-error":
			w.WriteHeader(500)
			fmt.Fprint(w, `{"error":"`+strings.Repeat("x", 8192)+`"}`)
		case "/redirect":
			w.Header().Set("Location", "/effect")
			w.WriteHeader(307)
		case "/effect":
			effects.Add(1)
			json.NewEncoder(w).Encode(d)
		case "/disabled":
			w.WriteHeader(204)
		case "/unexpected-disable-body":
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	client := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	var out channels.Delivery
	if err := client.callChannel(context.Background(), "GET", "/good", nil, &out); err != nil || !reflect.DeepEqual(d, out) {
		t.Fatal("exact receipt refused", err)
	}
	var httpErr *HTTPError
	if err := client.callChannel(context.Background(), "GET", "/missing", nil, &out); !errors.As(err, &httpErr) || httpErr.StatusCode != 404 {
		t.Fatal("exact bounded 404 lost", err)
	}
	for _, path := range []string{"/bad-missing", "/large-error", "/redirect"} {
		if err := client.callChannel(context.Background(), "POST", path, ChannelIDRequest{ID: "binding"}, &out); !errors.Is(err, errChannelResponse) {
			t.Fatal("ambiguous failure accepted", path, err)
		}
	}
	if effects.Load() != 0 {
		t.Fatal("redirect repeated effectful request")
	}
	if err := client.callChannel(context.Background(), "POST", "/disabled", nil, nil); err != nil {
		t.Fatal("empty disable response refused", err)
	}
	if err := client.callChannel(context.Background(), "POST", "/unexpected-disable-body", nil, nil); !errors.Is(err, errChannelResponse) {
		t.Fatal("unexpected disable payload accepted", err)
	}
}
