//go:build unix

package daemon

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TestDaemonCapabilitiesDeclareTerminalV1Unix pins that unix builds (with a
// real pty) advertise terminal-v1 on the WS handshake.
func TestDaemonCapabilitiesDeclareTerminalV1Unix(t *testing.T) {
	if !terminalCapabilityEnabled {
		t.Fatal("terminalCapabilityEnabled = false on unix")
	}
	if !strings.Contains(daemonClientCapabilities(), protocol.DaemonCapabilityTerminalV1) {
		t.Fatal("daemonClientCapabilities does not contain terminal-v1")
	}
}

// TestDaemonHTTPCapabilitiesDeclareTerminalV1Unix pins that the HTTP
// advertisement (X-Client-Capabilities on registration) also carries
// terminal-v1: the server stores per-runtime capabilities from that header and
// the terminal relay gates on the stored set, so a WS-only advertisement never
// reaches the runtime row and terminals fail closed (MAX-51).
func TestDaemonHTTPCapabilitiesDeclareTerminalV1Unix(t *testing.T) {
	if !terminalCapabilityEnabled {
		t.Fatal("terminalCapabilityEnabled = false on unix")
	}
	if !strings.Contains(daemonHTTPClientCapabilities(), protocol.DaemonCapabilityTerminalV1) {
		t.Fatal("daemonHTTPClientCapabilities does not contain terminal-v1")
	}
	if strings.Contains(daemonHTTPClientCapabilities(), protocol.DaemonCapabilityClaimPollHintsV1) {
		t.Fatal("daemonHTTPClientCapabilities must not contain claim-poll-hints-v1")
	}
}
