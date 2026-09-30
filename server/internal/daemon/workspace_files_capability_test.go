package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkspaceFilesCapabilityMatchesSafeOpener(t *testing.T) {
	hasCapability := func(value string) bool {
		for _, capability := range strings.Split(value, ",") {
			if strings.TrimSpace(capability) == protocol.DaemonCapabilityWorkspaceFilesV1 {
				return true
			}
		}
		return false
	}
	if hasCapability(daemonHTTPClientCapabilities()) != workspaceFilesCapabilityEnabled {
		t.Fatalf("HTTP declaration=%v, opener capability=%v", hasCapability(daemonHTTPClientCapabilities()), workspaceFilesCapabilityEnabled)
	}
	if hasCapability(daemonClientCapabilities()) != workspaceFilesCapabilityEnabled {
		t.Fatalf("WS declaration=%v, opener capability=%v", hasCapability(daemonClientCapabilities()), workspaceFilesCapabilityEnabled)
	}
}

func TestWorkspaceFilesClientProtocolNeverAcceptsRootPathOrDaemonID(t *testing.T) {
	body, err := json.Marshal(protocol.WorkspaceFilesClientListPayload{
		ClientReqID: "client-local-id", ResourceID: "resource", Path: "docs", PageSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"client_req_id":"client-local-id"`) || strings.Contains(string(body), "root_path") || strings.Contains(string(body), "daemon_req_id") {
		t.Fatalf("client request contract exposes trusted fields: %s", body)
	}
}

func TestWorkspaceFilesControlFramesDispatchThroughDaemonChannel(t *testing.T) {
	if !workspaceFilesCapabilityEnabled {
		t.Skip("safe workspace-files opener is unavailable on this platform")
	}
	root := fakeWorkspaceFilesDirectory()
	root.add("hello.txt", fakeWorkspaceFilesRegular([]byte("hello")))
	ch, out := newWorkspaceFilesTestChannel(root)
	frame, err := json.Marshal(protocol.Message{
		Type: protocol.EventWorkspaceFilesRead,
		Payload: marshalRaw(protocol.WorkspaceFilesReadPayload{
			DaemonReqID: "internal-id", RuntimeID: "runtime", ResourceID: "resource", RootPath: `C:\virtual`, Path: "hello.txt",
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var message protocol.Message
	if err := json.Unmarshal(frame, &message); err != nil {
		t.Fatal(err)
	}
	ch.HandleMessage(message.Type, message.Payload)
	result := workspaceFilesTestResult{}
	for {
		response := waitWorkspaceFilesFrame(t, out)
		var msg protocol.Message
		_ = json.Unmarshal(response, &msg)
		var payload protocol.WorkspaceFilesReadChunkPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.DaemonReqID != "internal-id" {
			t.Fatalf("response echoed unexpected request ID %q", payload.DaemonReqID)
		}
		result.data = append(result.data, payload.Data...)
		if payload.EOF {
			break
		}
	}
	if string(result.data) != "hello" {
		t.Fatalf("control frame response = %q", result.data)
	}
}
