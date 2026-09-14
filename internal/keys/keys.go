// Package keys translates key names into the byte sequences a terminal
// sends for them, so agents do not have to memorize terminfo: "Escape",
// ":", "w", "q", "Enter" instead of "\x1b:wq\r".
//
// The table covers what TUI driving actually needs — editing/navigation
// keys, F1–F12, and the Ctrl/Alt/Shift combinations — plus any printable
// character. Encoding follows the xterm defaults the browser side
// (xterm.js) also produces, so an agent-driven keypress is
// indistinguishable from a human one.
package keys

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// named maps a lower-cased key name to its byte sequence.
var named = map[string]string{
	// Editing / control
	"enter":     "\r",
	"return":    "\r",
	"tab":       "\t",
	"escape":    "\x1b",
	"esc":       "\x1b",
	"backspace": "\x7f",
	"bs":        "\x7f",
	"space":     " ",
	// Navigation / editing cluster
	"insert":   "\x1b[2~",
	"ins":      "\x1b[2~",
	"delete":   "\x1b[3~",
	"del":      "\x1b[3~",
	"home":     "\x1b[H",
	"end":      "\x1b[F",
	"pageup":   "\x1b[5~",
	"pgup":     "\x1b[5~",
	"pagedown": "\x1b[6~",
	"pgdn":     "\x1b[6~",
	"up":       "\x1b[A",
	"down":     "\x1b[B",
	"right":    "\x1b[C",
	"left":     "\x1b[D",
	// Function keys: F1–F4 are SS3, F5–F12 are CSI <n> ~
	"f1":  "\x1bOP",
	"f2":  "\x1bOQ",
	"f3":  "\x1bOR",
	"f4":  "\x1bOS",
	"f5":  "\x1b[15~",
	"f6":  "\x1b[17~",
	"f7":  "\x1b[18~",
	"f8":  "\x1b[19~",
	"f9":  "\x1b[20~",
	"f10": "\x1b[21~",
	"f11": "\x1b[23~",
	"f12": "\x1b[24~",
}

// ctrlRunes maps the non-alphabetic Ctrl combinations that do not follow
// the simple `rune & 0x1f` rule.
var ctrlRunes = map[rune]byte{
	'@':  0x00,
	' ':  0x00,
	'[':  0x1b,
	'\\': 0x1c,
	']':  0x1d,
	'^':  0x1e,
	'_':  0x1f,
	'?':  0x7f,
}

// arrows maps an arrow-key name to its CSI final byte, used to build the
// modified forms (Ctrl/Alt/Shift + arrow).
var arrows = map[string]byte{
	"up":    'A',
	"down":  'B',
	"right": 'C',
	"left":  'D',
}

// modParam maps a modifier to the xterm "CSI 1;<n><final>" parameter value
// (1 + shift(1) + alt(2) + ctrl(4)).
func modParam(shift, alt, ctrl bool) int {
	n := 1
	if shift {
		n += 1
	}
	if alt {
		n += 2
	}
	if ctrl {
		n += 4
	}
	return n
}

// Encode converts one key name into the bytes to write into the PTY.
//
// Accepted forms:
//
//	"Enter", "Tab", "Escape", "Backspace", "Space"
//	"Delete", "Insert", "Home", "End", "PgUp", "PgDn", "Up", "Down",
//	"Left", "Right", "F1"…"F12"
//	"Ctrl+C", "Ctrl+@", "Alt+X", "Shift+Tab", "Ctrl+Up"
//	any single printable character (":", "/", "w", "1", "你")
func Encode(name string) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("empty key name")
	}

	// A bare "+" is a legitimate printable key: only treat the name as a
	// modifier combination when it has a non-empty prefix part.
	var mods []string
	key := name
	if name != "+" {
		// readline-style shorthand: M-x = Alt+x, C-x = Ctrl+x.
		if !strings.Contains(name, "+") {
			switch {
			case strings.HasPrefix(name, "M-") && len(name) > 2:
				mods, key = []string{"alt"}, name[2:]
			case strings.HasPrefix(name, "C-") && len(name) > 2:
				mods, key = []string{"ctrl"}, name[2:]
			}
		} else if i := strings.LastIndex(name, "+"); i > 0 {
			mods = strings.Split(name[:i], "+")
			key = name[i+1:]
		}
	}

	var (
		shift, alt, ctrl bool
	)
	for _, m := range mods {
		switch strings.ToLower(strings.TrimSpace(m)) {
		case "ctrl", "control", "c":
			ctrl = true
		case "alt", "meta", "m", "option", "opt":
			alt = true
		case "shift", "s":
			shift = true
		default:
			return nil, fmt.Errorf("unknown key name %q: unknown modifier %q (known: %s)", name, m, strings.Join(modifierNames, ", "))
		}
	}
	if key == "" {
		return nil, fmt.Errorf("unknown key name %q: modifier without a key", name)
	}

	base, err := encodeBase(name, key, shift, alt, ctrl)
	if err != nil {
		return nil, err
	}

	// Alt+<non-arrow key> is ESC followed by the key's own bytes (the arrow
	// forms already carry the modifier in their CSI parameter).
	if alt {
		if _, isArrow := arrows[strings.ToLower(key)]; !isArrow {
			return append([]byte{0x1b}, base...), nil
		}
	}
	return base, nil
}

// encodeBase encodes the key part (modifiers already parsed).
func encodeBase(full, key string, shift, alt, ctrl bool) ([]byte, error) {
	lower := strings.ToLower(key)

	// Modified arrows: CSI 1;<mod><final>; with no modifier, CSI <final>.
	if final, ok := arrows[lower]; ok {
		if shift || alt || ctrl {
			mod := modParam(shift, alt, ctrl)
			return []byte(fmt.Sprintf("\x1b[1;%d%c", mod, final)), nil
		}
		return []byte(fmt.Sprintf("\x1b[%c", final)), nil
	}

	// Shift+Tab is Back Tab.
	if shift && lower == "tab" {
		return []byte("\x1b[Z"), nil
	}
	if (shift || ctrl || alt) && (lower == "home" || lower == "end") {
		final := byte('H')
		if lower == "end" {
			final = 'F'
		}
		return []byte(fmt.Sprintf("\x1b[1;%d%c", modParam(shift, alt, ctrl), final)), nil
	}

	// A named key (Alt prepends ESC afterwards; Shift/Ctrl have no standard
	// meaning for the remaining named keys).
	if !shift && !ctrl {
		if b, ok := named[lower]; ok {
			return []byte(b), nil
		}
	}

	// Ctrl + rune. Named single-character keys are resolved first, so
	// Ctrl+Space reaches the same code point as Ctrl+@.
	if ctrl {
		if b, ok := named[lower]; ok {
			if rs := []rune(b); len(rs) == 1 {
				if c, ok := ctrlRunes[rs[0]]; ok {
					return []byte{c}, nil
				}
			}
		}
		runes := []rune(key)
		if len(runes) != 1 || shift {
			return nil, fmt.Errorf("unknown key name %q: Ctrl+ takes a single character (e.g. Ctrl+C, Ctrl+@)", full)
		}
		r := unicode.ToLower(runes[0])
		if b, ok := ctrlRunes[r]; ok {
			return []byte{b}, nil
		}
		if r >= 'a' && r <= 'z' {
			return []byte{byte(r) & 0x1f}, nil
		}
		return nil, fmt.Errorf("unknown key name %q: no Ctrl encoding for %q", full, key)
	}

	// Shift + letter: the shifted character is its uppercase form.
	runes := []rune(key)
	if len(runes) != 1 {
		return nil, unknownKey(full)
	}
	r := runes[0]
	if shift && r >= 'a' && r <= 'z' {
		return []byte(string(unicode.ToUpper(r))), nil
	}
	if unicode.IsPrint(r) && r != ' ' {
		return []byte(string(r)), nil
	}
	return nil, unknownKey(full)
}

// unknownKey reports an unresolvable key name together with the candidate
// list, so callers never silently swallow a keypress.
func unknownKey(name string) error {
	return fmt.Errorf("unknown key name %q (known named keys: %s; also any single printable character, and Ctrl+/Alt+/Shift+ combinations)", name, strings.Join(Names(), ", "))
}

// aliases are accepted spellings that must not show up twice in Names().
var aliases = map[string]bool{
	"return": true, "esc": true, "bs": true,
	"ins": true, "del": true, "pgup": true, "pgdn": true,
}

// Names returns the canonical named keys, sorted — used for --help and for
// the candidate list in error messages. Aliases are folded into their
// canonical spelling.
func Names() []string {
	out := make([]string, 0, len(named))
	for name := range named {
		if aliases[name] {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// EncodeSequence encodes a key sequence in order (e.g. "Escape", ":", "w",
// "q", "Enter") and concatenates the results. An unknown name aborts the
// whole sequence — a half-applied key sequence is worse than none.
func EncodeSequence(names []string) ([]byte, error) {
	var out []byte
	for _, n := range names {
		b, err := Encode(n)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}

// modifierNames lists the accepted modifier spellings (for error messages).
var modifierNames = []string{"Ctrl", "Alt", "Shift"}
