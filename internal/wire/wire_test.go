package wire_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kurisu-agent/drift/internal/wire"
)

func TestDecodeRequest_valid(t *testing.T) {
	body := `{"jsonrpc":"2.0","method":"kart.list","params":{"circuit":"x"},"id":1}`
	got, err := wire.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if got.Method != "kart.list" {
		t.Errorf("method = %q, want kart.list", got.Method)
	}
	if string(got.ID) != "1" {
		t.Errorf("id = %q, want 1", got.ID)
	}
}

func TestDecodeRequest_errors(t *testing.T) {
	cases := map[string]string{
		"wrong version":  `{"jsonrpc":"1.0","method":"x","id":1}`,
		"missing method": `{"jsonrpc":"2.0","id":1}`,
		"missing id":     `{"jsonrpc":"2.0","method":"x"}`,
		"positional":     `{"jsonrpc":"2.0","method":"x","params":[1,2],"id":1}`,
		"unknown field":  `{"jsonrpc":"2.0","method":"x","id":1,"extra":1}`,
		"not json":       `not json`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := wire.DecodeRequest(strings.NewReader(body)); err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

func TestEncodeResponse_roundtrip(t *testing.T) {
	resp := &wire.Response{
		Result: json.RawMessage(`{"ok":true}`),
		ID:     json.RawMessage(`1`),
	}
	var buf bytes.Buffer
	if err := wire.EncodeResponse(&buf, resp); err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		t.Errorf("response must be newline-terminated, got %q", buf.String())
	}
	got, err := wire.DecodeResponse(&buf)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	want := &wire.Response{
		JSONRPC: wire.Version,
		Result:  json.RawMessage(`{"ok":true}`),
		ID:      json.RawMessage(`1`),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("round-trip mismatch (-want +got):\n%s", diff)
	}
}

func TestDecodeResponse_bothResultAndErrorRejected(t *testing.T) {
	body := `{"jsonrpc":"2.0","result":{},"error":{"code":1,"message":"x"},"id":1}`
	if _, err := wire.DecodeResponse(strings.NewReader(body)); err == nil {
		t.Fatal("expected error when both result and error set")
	}
}

func FuzzDecodeRequest(f *testing.F) {
	f.Add(`{"jsonrpc":"2.0","method":"x","id":1}`)
	f.Add(`{"jsonrpc":"2.0","method":"x","params":{},"id":"s"}`)
	f.Add(``)
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = wire.DecodeRequest(strings.NewReader(s))
	})
}
