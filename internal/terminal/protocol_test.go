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

func TestEncodeReplayDone(t *testing.T) {
	frame := EncodeReplayDone()
	if len(frame) != 1 || frame[0] != SetReplayDone {
		t.Fatalf("unexpected replay-done frame: %v", frame)
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

func TestRouteFrameRoundTrip(t *testing.T) {
	sid := []byte("a00000000000000a") // 16 base36 chars
	payload := []byte("hello terminal")

	frame, err := EncodeRouteFrame(sid, Output, payload)
	if err != nil {
		t.Fatalf("EncodeRouteFrame: %s", err)
	}
	// header = 16 (sid) + 1 (type) + 2 (len) = 19
	if len(frame) != RouteHeaderLen+len(payload) {
		t.Fatalf("frame length %d, want %d", len(frame), RouteHeaderLen+len(payload))
	}

	gotSID, msg, err := DecodeRouteFrame(frame)
	if err != nil {
		t.Fatalf("DecodeRouteFrame: %s", err)
	}
	if !bytes.Equal(gotSID, sid) {
		t.Fatalf("sid = %q, want %q", gotSID, sid)
	}
	if msg.Type != Output {
		t.Fatalf("type = %c, want %c", msg.Type, Output)
	}
	if !bytes.Equal(msg.Payload, payload) {
		t.Fatalf("payload = %q, want %q", msg.Payload, payload)
	}
}

func TestRouteFrameConnectionLevel(t *testing.T) {
	zero := make([]byte, RouteSessionIDLen)
	if !IsConnectionLevel(zero) {
		t.Fatal("all-zero id must be connection-level")
	}
	if IsConnectionLevel([]byte("a00000000000000a")) {
		t.Fatal("a real session id must not be connection-level")
	}

	frame, err := EncodeRouteFrame(zero, Ping, nil)
	if err != nil {
		t.Fatalf("EncodeRouteFrame conn-level: %s", err)
	}
	gotSID, msg, err := DecodeRouteFrame(frame)
	if err != nil {
		t.Fatalf("DecodeRouteFrame conn-level: %s", err)
	}
	if !IsConnectionLevel(gotSID) {
		t.Fatal("decoded sid must round-trip as connection-level")
	}
	if msg.Type != Ping {
		t.Fatalf("type = %c, want %c", msg.Type, Ping)
	}
}

func TestRouteFrameLengthMismatch(t *testing.T) {
	sid := []byte("a00000000000000a")
	good, _ := EncodeRouteFrame(sid, Output, []byte("abc"))
	bad := make([]byte, len(good))
	copy(bad, good)
	// 篡改声明长度(实际 payload 为 3 字节),解码必须报错
	bad[RouteSessionIDLen+1] = 0
	bad[RouteSessionIDLen+2] = 99
	if _, _, err := DecodeRouteFrame(bad); err == nil {
		t.Fatal("expected length-mismatch error")
	}
}

func TestRouteFrameTooShort(t *testing.T) {
	if _, _, err := DecodeRouteFrame([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected too-short error")
	}
}

func TestEncodeRouteFrameBadSID(t *testing.T) {
	if _, err := EncodeRouteFrame([]byte("short"), Output, nil); err == nil {
		t.Fatal("expected bad-session-id error")
	}
}

func TestEncodeRouteFramePayloadTooLarge(t *testing.T) {
	sid := make([]byte, RouteSessionIDLen)
	big := make([]byte, MaxRoutePayload+1)
	if _, err := EncodeRouteFrame(sid, Output, big); err == nil {
		t.Fatal("expected payload-too-large error")
	}
}
