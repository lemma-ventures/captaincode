package captaincode

import "unicode/utf8"

// String cuts that never split a character. A prompt windowed at a byte
// offset landed inside a multibyte rune ("→", "·", "—" are all over a replayed
// transcript) and reached codex exec as invalid UTF-8: its Rust CLI refuses
// any such argument ("invalid UTF-8 was detected in one or more arguments",
// live 2026-09-15). claude -p and opencode had been swallowing the damage.

// CutHead returns at most n bytes from the start of s, ending on a rune boundary.
func CutHead(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// CutTail returns at most n bytes from the end of s, starting on a rune boundary.
func CutTail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}
