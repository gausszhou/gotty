package keys

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncodeNamedKeys(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"Enter", "\r"},
		{"enter", "\r"},
		{"Return", "\r"},
		{"Tab", "\t"},
		{"Escape", "\x1b"},
		{"Esc", "\x1b"},
		{"Backspace", "\x7f"},
		{"Space", " "},
		{"Delete", "\x1b[3~"},
		{"Insert", "\x1b[2~"},
		{"Home", "\x1b[H"},
		{"End", "\x1b[F"},
		{"PgUp", "\x1b[5~"},
		{"PageDown", "\x1b[6~"},
		{"Up", "\x1b[A"},
		{"Down", "\x1b[B"},
		{"Right", "\x1b[C"},
		{"Left", "\x1b[D"},
		{"F1", "\x1bOP"},
		{"F4", "\x1bOS"},
		{"F5", "\x1b[15~"},
		{"F12", "\x1b[24~"},
	}
	for _, tc := range cases {
		got, err := Encode(tc.name)
		if err != nil {
			t.Errorf("Encode(%q) error: %v", tc.name, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("Encode(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEncodePrintableKeys(t *testing.T) {
	for _, k := range []string{":", "/", "!", "@", "w", "q", "1", "你", "+", "%", "."} {
		got, err := Encode(k)
		if err != nil {
			t.Errorf("Encode(%q) error: %v", k, err)
			continue
		}
		if string(got) != k {
			t.Errorf("Encode(%q) = %q, want itself", k, got)
		}
	}
}

func TestEncodeCtrlCombinations(t *testing.T) {
	cases := []struct {
		name string
		want []byte
	}{
		{"Ctrl+C", []byte{0x03}},
		{"ctrl+a", []byte{0x01}},
		{"Control+Z", []byte{0x1a}},
		{"Ctrl+@", []byte{0x00}},
		{"Ctrl+Space", []byte{0x00}},
		{"Ctrl+[", []byte{0x1b}},
		{"Ctrl+\\", []byte{0x1c}},
		{"Ctrl+]", []byte{0x1d}},
		{"Ctrl+^", []byte{0x1e}},
		{"Ctrl+_", []byte{0x1f}},
		{"Ctrl+Up", []byte("\x1b[1;5A")},
		{"Ctrl+Right", []byte("\x1b[1;5C")},
	}
	for _, tc := range cases {
		got, err := Encode(tc.name)
		if err != nil {
			t.Errorf("Encode(%q) error: %v", tc.name, err)
			continue
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("Encode(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEncodeAltAndShift(t *testing.T) {
	cases := []struct {
		name string
		want []byte
	}{
		{"Alt+x", []byte("\x1bx")},
		{"Alt+Enter", []byte("\x1b\r")},
		{"M-x", []byte("\x1bx")},
		{"Alt+Up", []byte("\x1b[1;3A")},
		{"Alt+Ctrl+Left", []byte("\x1b[1;7D")},
		{"Shift+Tab", []byte("\x1b[Z")},
		{"Shift+a", []byte("A")},
		{"Shift+Shift+Tab", []byte("\x1b[Z")},
		{"Shift+Home", []byte("\x1b[1;2H")},
		{"Alt+Ctrl+x", []byte("\x1b\x18")},
	}
	for _, tc := range cases {
		got, err := Encode(tc.name)
		if err != nil {
			t.Errorf("Encode(%q) error: %v", tc.name, err)
			continue
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("Encode(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEncodeUnknownKeyReportsCandidates(t *testing.T) {
	// Names that fail for a generic reason must list the candidate keys, so
	// an agent can self-correct instead of guessing again.
	for _, bad := range []string{"F13", "Unknown", "NoSuchKey"} {
		_, err := Encode(bad)
		if err == nil {
			t.Errorf("Encode(%q) unexpectedly succeeded", bad)
			continue
		}
		if !strings.Contains(err.Error(), "enter") {
			t.Errorf("Encode(%q) error %q does not list candidate keys", bad, err)
		}
	}

	// Structural mistakes report their own message (no candidate list needed),
	// but must still be errors — never a silent empty write.
	for _, bad := range []string{"", "Ctrl+Foo", "Alt+Ctrl+Foo", "NoSuch+Key"} {
		if _, err := Encode(bad); err == nil {
			t.Errorf("Encode(%q) unexpectedly succeeded", bad)
		}
	}
}

func TestEncodeSequence(t *testing.T) {
	got, err := EncodeSequence([]string{"Escape", ":", "w", "q", "Enter"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "\x1b:wq\r"; string(got) != want {
		t.Errorf("EncodeSequence = %q, want %q", got, want)
	}

	// A bad name aborts the whole sequence (no partial write).
	if _, err := EncodeSequence([]string{"Escape", "Bogus"}); err == nil {
		t.Error("EncodeSequence with a bad name should fail")
	}
}

func TestNamesExcludeAliases(t *testing.T) {
	names := Names()
	set := map[string]bool{}
	for _, n := range names {
		if set[n] {
			t.Errorf("Names() contains %q twice", n)
		}
		set[n] = true
	}
	for _, alias := range []string{"return", "esc", "bs", "ins", "del", "pgup", "pgdn"} {
		if set[alias] {
			t.Errorf("Names() should not list alias %q", alias)
		}
	}
	for _, canonical := range []string{"enter", "escape", "backspace", "f1", "pagedown"} {
		if !set[canonical] {
			t.Errorf("Names() missing canonical key %q", canonical)
		}
	}
}
