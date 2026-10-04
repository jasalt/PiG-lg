//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

const ptyStartupMarker = "pty-session-start-ready"

// ptyStartupProbe observes session_start, which interactive-mode.ts emits after managed-tool setup and installation of the ordinary submit handler. The header and model footer are drawn before that boundary.
func ptyStartupProbe(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "startup-ready.mjs")
	if err := os.WriteFile(path, []byte(`export default function (pi) {
 pi.on("session_start", (_event, ctx) => ctx.ui.notify("pty-session-start-ready", "info"));
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
