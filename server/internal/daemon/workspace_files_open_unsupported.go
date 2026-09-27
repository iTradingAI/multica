//go:build !linux && !windows

package daemon

import "context"

type unsupportedWorkspaceFilesOpener struct{}

func newWorkspaceFilesOpener() workspaceFilesOpener { return unsupportedWorkspaceFilesOpener{} }

var workspaceFilesCapabilityEnabled = false

func (unsupportedWorkspaceFilesOpener) OpenRoot(context.Context, string) (workspaceFilesHandle, error) {
	return nil, workspaceFilesError("unsupported")
}
