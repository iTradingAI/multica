//go:build !unix

package daemon

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TestDaemonCapabilitiesOmitTerminalV1WithoutPty pins that platforms without
// a pty implementation never advertise terminal-v1, so the server relay
// refuses terminal subscriptions for their runtimes.
func TestDaemonCapabilitiesOmitTerminalV1WithoutPty(t *testing.T) {
	if terminalCapabilityEnabled {
		t.Fatal("terminalCapabilityEnabled = true without a pty implementation")
	}
	if strings.Contains(daemonClientCapabilities(), protocol.DaemonCapabilityTerminalV1) {
		t.Fatal("daemonClientCapabilities must not contain terminal-v1")
	}
}
