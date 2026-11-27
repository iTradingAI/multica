//go:build !windows

package daemon

import (
	"context"
	"encoding/json"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const workspaceFilesEditCapabilityEnabled = false

func performWorkspaceFilesEdit(context.Context, protocol.WorkspaceFilesEditPayload, *workspaceFilesCursorSigner, bool) (json.RawMessage, error) {
	return nil, workspaceFilesError("unsupported")
}
