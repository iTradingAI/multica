//go:build !linux && !windows

package daemon

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkspaceFilesUnsupportedPlatformDoesNotAdvertiseCapability(t *testing.T) {
	if workspaceFilesCapabilityEnabled {
		t.Fatal("unsupported platform enabled workspace-files")
	}
	for _, value := range []string{daemonHTTPClientCapabilities(), daemonClientCapabilities()} {
		if strings.Contains(value, protocol.DaemonCapabilityWorkspaceFilesV1) {
			t.Fatalf("unsupported platform advertised workspace-files: %q", value)
		}
	}
}
