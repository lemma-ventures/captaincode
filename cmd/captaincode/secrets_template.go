package main

import "io"

func secretsTemplate(out io.Writer) error {
	const settings = "CAPTAIN_AUTO_UPGRADE=all\n" +
		"CAPTAIN_REDACT=on\n" +
		"CAPTAIN_PROXY_ADDR=127.0.0.1:14098\n" +
		"CAPTAIN_PROXY_CLAUDE=1\n" +
		"CAPTAIN_PROXY_CODEX=1\n"
	_, err := io.WriteString(out, settings)
	return err
}
