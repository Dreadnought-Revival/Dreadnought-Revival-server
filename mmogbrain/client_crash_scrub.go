package main

// Same-length scrubbing of crash report files (client_crashes.go).
//
// A crash report is binary: a minidump is a table of offsets into itself, so
// nothing may move. Every rule here overwrites the private text IN PLACE with
// '*' of the same length, in both encodings the game's memory holds text in:
// single-byte (UTF-8/ANSI) and UTF-16LE (UE4's TCHAR, Windows' own strings).
// The launcher applies the same rules, plus this PC's own names
// (Dreadnought-Revival-Launcher crashupload/scrub.go; keep the two in step);
// this side runs them again so a launcher with an older or broken filter
// cannot store them here. Removed:
//
//   - sign-in tokens: anything shaped like a JWT ("eyJ" + base64url, up to
//     two dots). The client keeps its own on the command line and prints it;
//   - the Windows user folder name in paths (...\Users\<name>\...);
//   - the computer and user names UE4 prints ("LogInit: Computer: ...",
//     "LogInit: User: ...") and the environment variables naming them
//     (USERNAME=, COMPUTERNAME=, USERDOMAIN=, LOGONSERVER=, USERPROFILE=,
//     HOMEPATH=, ...), up to the end of the line or the string.

// crashScrubLineKeys are prefixes whose value, to the end of the line or the
// string, is private.
var crashScrubLineKeys = []string{
	"LogInit: Computer: ", "LogInit: User: ",
	"USERNAME=", "COMPUTERNAME=", "USERDOMAIN=", "USERDOMAIN_ROAMINGPROFILE=",
	"LOGONSERVER=", "USERPROFILE=", "HOMEPATH=", "ONEDRIVE=", "ONEDRIVECONSUMER=",
}

// scrubCrashData overwrites the private parts of b in place. needles are
// further strings to remove wherever they appear, case-insensitively.
func scrubCrashData(b []byte, needles []string) {
	for _, w := range []int{1, 2} {
		scrubCrashJWTs(b, w)
		scrubCrashUserDirs(b, w)
		for _, k := range crashScrubLineKeys {
			scrubCrashAfterKey(b, w, k)
		}
		for _, n := range needles {
			scrubCrashNeedle(b, w, n)
		}
	}
}

// crashUnit is the character at k in width w (1: a byte; 2: a UTF-16LE code
// unit whose high byte must be 0 to count as ASCII). ok is false past the end
// or, for w 2, on a non-ASCII unit.
func crashUnit(b []byte, k, w int) (byte, bool) {
	if k < 0 || k+w > len(b) {
		return 0, false
	}
	if w == 2 && b[k+1] != 0 {
		return 0, false
	}
	return b[k], true
}

func crashStar(b []byte, from, to, w int) {
	for k := from; k+w <= to; k += w {
		b[k] = '*'
		if w == 2 {
			b[k+1] = 0
		}
	}
}

func crashB64(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

func crashLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// crashMatchFold reports whether s (ASCII) is at k in width w, ignoring case.
func crashMatchFold(b []byte, k, w int, s string) bool {
	for i := 0; i < len(s); i++ {
		c, ok := crashUnit(b, k+i*w, w)
		if !ok || crashLower(c) != crashLower(s[i]) {
			return false
		}
	}
	return true
}

// scrubCrashJWTs: "eyJ" followed by at least 6 base64url characters, then up
// to two more dot-separated segments.
func scrubCrashJWTs(b []byte, w int) {
	for i := 0; i+3*w <= len(b); i++ {
		if b[i] != 'e' || !crashMatchFold(b, i, w, "eyJ") || b[i+w] != 'y' || b[i+2*w] != 'J' {
			continue
		}
		k, run, dots := i+3*w, 0, 0
		for {
			c, ok := crashUnit(b, k, w)
			if !ok {
				break
			}
			if crashB64(c) {
				run++
			} else if c == '.' && dots < 2 {
				dots++
			} else {
				break
			}
			k += w
		}
		if run >= 6 {
			crashStar(b, i, k, w)
			i = k - 1
		}
	}
}

// crashPathEnd reports whether the unit at k ends a path component.
func crashPathEnd(b []byte, k, w int) bool {
	if k+w > len(b) {
		return true
	}
	if w == 2 && b[k+1] != 0 {
		return false // a non-ASCII character: part of the name
	}
	switch c := b[k]; {
	case c < 0x20, c == '\\', c == '/', c == '"', c == '\'', c == '<', c == '>', c == '|', c == '?', c == '*', c == ':':
		return true
	}
	return false
}

// scrubCrashUserDirs: the component after a "Users" path component.
func scrubCrashUserDirs(b []byte, w int) {
	for i := w; i+6*w <= len(b); i++ {
		if crashLower(b[i]) != 'u' || !crashMatchFold(b, i, w, "users") {
			continue
		}
		if c, ok := crashUnit(b, i-w, w); !ok || c != '\\' && c != '/' {
			continue
		}
		k := i + 5*w
		if c, ok := crashUnit(b, k, w); !ok || c != '\\' && c != '/' {
			continue
		}
		for {
			c, ok := crashUnit(b, k, w)
			if !ok || c != '\\' && c != '/' {
				break
			}
			k += w
		}
		start, n := k, 0
		for !crashPathEnd(b, k, w) && n < 128 {
			k += w
			n++
		}
		if n > 0 {
			crashStar(b, start, k, w)
		}
		i = k - 1
	}
}

// scrubCrashAfterKey: the value after key, to the end of the line or string.
func scrubCrashAfterKey(b []byte, w int, key string) {
	first := crashLower(key[0])
	for i := 0; i+len(key)*w <= len(b); i++ {
		if crashLower(b[i]) != first || !crashMatchFold(b, i, w, key) {
			continue
		}
		start := i + len(key)*w
		k := start
		for n := 0; k+w <= len(b) && n < 260; n++ {
			if b[k] == 0 && (w == 1 || b[k+1] == 0) || (w == 1 || b[k+1] == 0) && (b[k] == '\r' || b[k] == '\n') {
				break
			}
			k += w
		}
		crashStar(b, start, k, w)
		i = k - 1
	}
}

// scrubCrashNeedle removes s wherever it appears. For w 2 a non-ASCII s is
// matched exactly as UTF-16LE; ASCII letters ignore case in both widths.
func scrubCrashNeedle(b []byte, w int, s string) {
	if s == "" {
		return
	}
	units := []uint16{}
	for _, r := range s {
		if r > 0xFFFF {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			units = append(units, uint16(r))
		}
	}
	var pat []byte
	if w == 1 {
		pat = []byte(s)
	} else {
		for _, u := range units {
			pat = append(pat, byte(u), byte(u>>8))
		}
	}
	for i := 0; i+len(pat) <= len(b); i++ {
		match := true
		for j := 0; j < len(pat); j++ {
			x, y := b[i+j], pat[j]
			if x != y && (crashLower(x) != crashLower(y) || x|0x20 < 'a' || x|0x20 > 'z') {
				match = false
				break
			}
		}
		if match {
			for j := 0; j < len(pat); j++ {
				if w == 1 || j%2 == 0 {
					b[i+j] = '*'
				} else {
					b[i+j] = 0
				}
			}
			i += len(pat) - 1
		}
	}
}
