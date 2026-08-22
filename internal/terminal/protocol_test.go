package terminal

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestEncodeOutput(t *testing.T) {
	frame := EncodeOutput([]byte("foobar"))
	if len(frame) != 7 || frame[0] != Output {
		t.Fatalf("unexpected frame: %v", frame)
	}
	if !bytes.Equal(frame[1:], []byte("foobar")) {
		t.Fatalf("unexpected payload: %v", frame[1:])
	}
}

func TestEncodeWindowTitle(t *testing.T) {
	frame := EncodeWindowTitle([]byte("GoTTY - bash@host"))
	if frame[0] != SetWindowTitle {
		t.Fatalf("unexpected message type `%c`", frame[0])
	}
	if string(frame[1:]) != "GoTTY - bash@host" {
		t.Fatalf("unexpected title: %q", frame[1:])
	}
}

func TestEncodeReconnect(t *testing.T) {
	frame := EncodeReconnect(10)
	if frame[0] != SetReconnect {
		t.Fatalf("unexpected message type `%c`", frame[0])
	}
	var seconds int
	if err := json.Unmarshal(frame[1:], &seconds); err != nil {
		t.Fatalf("failed to unmarshal reconnect payload: %s", err)
	}
	if seconds != 10 {
		t.Fatalf("unexpected reconnect seconds: %d", seconds)
	}
}

func TestDecodeClientFrame(t *testing.T) {
	cases := []struct {
		name    string
		frame   []byte
		msgType byte
		payload []byte
		wantErr bool
	}{
		{"input", []byte{Input, 'a', 'b'}, Input, []byte{'a', 'b'}, false},
		{"ping", []byte{Ping}, Ping, nil, false},
		{"resize", []byte{ResizeTerminal, '{', '}'}, ResizeTerminal, []byte{'{', '}'}, false},
		{"empty", []byte{}, 0, nil, true},
		{"unknown type", []byte{'x'}, 0, nil, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := DecodeClientFrame(tc.frame)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if msg.Type != tc.msgType {
				t.Fatalf("unexpected type `%c`", msg.Type)
			}
			if !bytes.Equal(msg.Payload, tc.payload) {
				t.Fatalf("unexpected payload: %v", msg.Payload)
			}
		})
	}
}

func TestParseResizeArgs(t *testing.T) {
	// encoding/json matches keys case-insensitively,
	// so both spellings used by clients work.
	for _, payload := range []string{
		`{"columns":120,"rows":40}`,
		`{"Columns":120,"Rows":40}`,
	} {
		args, err := ParseResizeArgs([]byte(payload))
		if err != nil {
			t.Fatalf("failed to parse %s: %s", payload, err)
		}
		if args.Columns != 120 || args.Rows != 40 {
			t.Fatalf("unexpected args from %s: %+v", payload, args)
		}
	}

	if _, err := ParseResizeArgs([]byte("not json")); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
