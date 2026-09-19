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
