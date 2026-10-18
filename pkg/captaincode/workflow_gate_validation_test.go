package captaincode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitGateKeepsQuotedMarkersAndParagraphs(t *testing.T) {
	cases := []struct{ text, prompt, gate string }{
		{"explain `gate: true`", "explain `gate: true`", ""},
		{"delegate: true", "delegate: true", ""},
		{"fix it\r\n\r\nreport gate: false", "fix it\r\n\r\nreport gate: false", ""},
		{"fix it gate: true\r\n\t\r\nreport gate: false", "fix it\r\n\t\r\nreport gate: false", "true"},
		{"fix it\ngate: true", "fix it", "true"},
		{"fix it GATE: true", "fix it", "true"},
		{"explain `gate: false` gate: true", "explain `gate: false`", "true"},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			prompt, gate, err := SplitGate(c.text)
			require.NoError(t, err)
			assert.Equal(t, c.prompt, prompt)
			assert.Equal(t, c.gate, gate)
		})
	}
}

func TestSplitGateValidatesLiteralExecutable(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"checktool", "check tool"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "not-executable"), nil, 0o644))
	t.Setenv("PATH", dir)
	for _, command := range []string{
		"checktool --all",
		"[ -f marker ]",
		`"checktool" --all`,
		`'checktool' --all`,
		`check\tool --all`,
		`LABEL='two words' checktool --all`,
		`"` + filepath.Join(dir, "check tool") + `" --all`,
		"checktool&&checktool --all",
	} {
		t.Run(command, func(t *testing.T) {
			_, gate, err := SplitGate("fix it gate: " + command)
			require.NoError(t, err)
			assert.Equal(t, command, gate)
		})
	}
	for _, command := range []string{
		"unknown-checker --all",
		`"unknown-checker" --all`,
		`'unknown-checker' --all`,
		`\unknown-checker --all`,
		filepath.Join(dir, "missing"),
		filepath.Join(dir, "not-executable"),
		`"$CHECKER" --all`,
		`$(printf checktool) --all`,
		`! unknown-checker`,
		`(unknown-checker)`,
		`"checktool`,
		`'LABEL=value' checktool`,
	} {
		t.Run(command, func(t *testing.T) {
			text := "fix it gate: " + command
			prompt, gate, err := SplitGate(text)
			require.ErrorIs(t, err, ErrGateCommand)
			assert.Equal(t, text, prompt)
			assert.Empty(t, gate)
		})
	}
}

func TestLanguageGateParagraphs(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		report := "/codex check the report:" + ending +
			"Local gate: default suite passed." + ending +
			"All checks completed."
		turn := ParseTurn(report)
		assert.Equal(t, "leg", turn.Kind)
		assert.Contains(t, turn.Text, "Local gate: default suite passed.")
		assert.Empty(t, turn.Gate)

		quoted := "/codex fix the parser:" + ending +
			"- Accept gate: only on the stage's last line." + ending +
			"- Reject a command that is not on PATH."
		turn = ParseTurn(quoted)
		assert.Equal(t, "leg", turn.Kind)
		assert.Contains(t, turn.Text, "- Accept gate: only on the stage's last line.")
		assert.Empty(t, turn.Gate)
	}
}
