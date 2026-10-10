package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/channels"
)

// A full history page can contain 100 16 KiB texts escaped six bytes per
// character. Oversized status/error responses fail closed rather than allocating
// from a daemon's unbounded document. This is channel-specific: older protocol
// methods retain their existing behavior.
const maxChannelResponseBytes = 24 << 20
const maxChannelResponseNodes = 100000

var errChannelResponse = errors.New("channel response could not be verified")
var channelTimeType = reflect.TypeFor[time.Time]()

// callChannel decodes into a private temporary value and publishes it only after
// one bounded, UTF-8, unambiguous document passes the exact response schema.
func (c *Client) callChannel(ctx context.Context, method, path string, in, out any) error {
	var input bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&input).Encode(in); err != nil {
			return errChannelResponse
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, &input)
	if err != nil || c.HTTP == nil {
		return errChannelResponse
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Backplane-Protocol", Version)
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout <= 0 || client.Timeout > 20*time.Second {
		client.Timeout = 20 * time.Second
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	bound := maxChannelResponseBytes
	if response.StatusCode >= 300 {
		bound = 8192
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, int64(bound)+1))
	if err != nil || len(raw) > bound || !utf8.Valid(raw) {
		return errChannelResponse
	}
	if response.StatusCode >= 300 {
		var failure Error
		if decodeChannelResponse(raw, &failure) != nil {
			return errChannelResponse
		}
		return &HTTPError{StatusCode: response.StatusCode, Message: failure.Message}
	}
	if response.StatusCode < 200 {
		return errChannelResponse
	}
	if out == nil {
		if len(bytes.TrimSpace(raw)) != 0 {
			return errChannelResponse
		}
		return nil
	}
	return decodeChannelResponse(raw, out)
}

func decodeChannelResponse(raw []byte, out any) error {
	value := reflect.ValueOf(out)
	if len(raw) > maxChannelResponseBytes || !utf8.Valid(raw) || !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return errChannelResponse
	}
	schema := value.Elem().Type()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	var walk func(reflect.Type, string, int) error
	walk = func(schema reflect.Type, field string, depth int) error {
		nodes++
		if depth > 32 || nodes > maxChannelResponseNodes {
			return errChannelResponse
		}
		nullable := schema.Kind() == reflect.Pointer || schema.Kind() == reflect.Slice || schema.Kind() == reflect.Map
		for schema.Kind() == reflect.Pointer {
			schema = schema.Elem()
		}
		token, err := decoder.Token()
		if err != nil {
			return errChannelResponse
		}
		if token == nil {
			if nullable {
				return nil
			}
			return errChannelResponse
		}
		if schema == channelTimeType {
			s, ok := token.(string)
			if !ok || len(s) > 64 {
				return errChannelResponse
			}
			return nil
		}
		switch schema.Kind() {
		case reflect.Struct:
			if token != json.Delim('{') {
				return errChannelResponse
			}
			fields := map[string]reflect.StructField{}
			for i := 0; i < schema.NumField(); i++ {
				f := schema.Field(i)
				if f.PkgPath != "" {
					continue
				}
				name := strings.Split(f.Tag.Get("json"), ",")[0]
				if name == "-" {
					continue
				}
				if name == "" {
					name = f.Name
				}
				fields[name] = f
			}
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				name, ok := key.(string)
				f, exists := fields[name]
				if err != nil || !ok || !exists || seen[name] {
					return errChannelResponse
				}
				seen[name] = true
				if err := walk(f.Type, f.Name, depth+1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errChannelResponse
			}
		case reflect.Map:
			if token != json.Delim('{') || schema.Key().Kind() != reflect.String {
				return errChannelResponse
			}
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				name, ok := key.(string)
				// Map keys are case-sensitive identities, not schema field spellings.
				if err != nil || !ok || !channels.ValidID(name) || seen[name] || len(seen) >= 256 {
					return errChannelResponse
				}
				seen[name] = true
				if err := walk(schema.Elem(), field, depth+1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errChannelResponse
			}
		case reflect.Slice, reflect.Array:
			if token != json.Delim('[') {
				return errChannelResponse
			}
			bound := channels.MaxPageSize
			if schema.Kind() == reflect.Array {
				bound = schema.Len()
			} else if schema.Elem() == reflect.TypeFor[ChannelStatus]() {
				bound = 128
			} else if field == "Allowlist" {
				bound = 256
			}
			count := 0
			for decoder.More() {
				if count >= bound {
					return errChannelResponse
				}
				if err := walk(schema.Elem(), field, depth+1); err != nil {
					return err
				}
				count++
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') || schema.Kind() == reflect.Array && count != schema.Len() {
				return errChannelResponse
			}
		case reflect.String:
			s, ok := token.(string)
			bound := 1024
			if field == "Text" {
				bound = channels.MaxTextBytes
			} else if schema == reflect.TypeFor[string]() && field == "Message" {
				bound = 4096
			}
			if !ok || len(s) > bound {
				return errChannelResponse
			}
		case reflect.Bool:
			if _, ok := token.(bool); !ok {
				return errChannelResponse
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
			if _, ok := token.(json.Number); !ok {
				return errChannelResponse
			}
		default:
			return errChannelResponse
		}
		return nil
	}
	if walk(schema, "", 0) != nil {
		return errChannelResponse
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errChannelResponse
	}
	temporary := reflect.New(schema)
	if json.Unmarshal(raw, temporary.Interface()) != nil {
		return errChannelResponse
	}
	value.Elem().Set(temporary.Elem())
	return nil
}
