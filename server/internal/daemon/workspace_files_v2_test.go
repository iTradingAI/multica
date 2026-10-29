package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkspaceFilesDaemonCapabilities(t *testing.T) {
	for _, caps := range []string{daemonHTTPClientCapabilities(), daemonClientCapabilities()} {
		for _, version := range []string{protocol.DaemonCapabilityWorkspaceFilesV1, protocol.DaemonCapabilityWorkspaceFilesV2} {
			found := false
			for _, token := range strings.Split(caps, ",") {
				if token == version {
					found = true
				}
			}
			if found != workspaceFilesCapabilityEnabled {
				t.Fatal("workspace-files capability does not match platform support", version)
			}
		}
	}
}

func TestWorkspaceFilesV2UnsupportedEcho(t *testing.T) {
	ch, out := newWorkspaceFilesTestChannel(fakeWorkspaceFilesDirectory())
	ch.opening = nil
	g := protocol.WorkspaceFilesGeneration{ConnectionEpoch: 42, RelaySeq: 74}
	raw, _ := json.Marshal(protocol.WorkspaceFilesReadPayload{WorkspaceFilesGeneration: g, DaemonReqID: "id", RuntimeID: "runtime", ResourceID: "resource", RootPath: "private-root", Path: "file.txt"})
	ch.HandleMessage(protocol.EventWorkspaceFilesRead, raw)
	select {
	case raw := <-out:
		var m protocol.Message
		var p protocol.WorkspaceFilesErrorPayload
		if json.Unmarshal(raw, &m) != nil || json.Unmarshal(m.Payload, &p) != nil || p.WorkspaceFilesGeneration != g || p.Code != "unsupported" || strings.Contains(string(raw), "private-root") {
			t.Fatal("unsupported response association/projection")
		}
	case <-time.After(time.Second):
		t.Fatal("unsupported response timeout")
	}
}

func TestWorkspaceFilesV2GenerationEchoAndCancel(t *testing.T) {
	g := protocol.WorkspaceFilesGeneration{ConnectionEpoch: 42, RelaySeq: 73}
	reads, err := buildWorkspaceFilesReadFrames("req", "runtime", "resource", []byte("text"), g)
	if err != nil {
		t.Fatal(err)
	}
	lists, err := buildWorkspaceFilesListFrames("req", "runtime", "resource", []protocol.WorkspaceFilesEntry{{Name: "file", Type: "regular"}}, "", 0, g)
	if err != nil {
		t.Fatal(err)
	}
	frames := append(reads, lists...)
	frames = append(frames, buildWorkspaceFilesErrorFrame("req", "runtime", "resource", "unsupported", g))
	for _, raw := range frames {
		var msg protocol.Message
		var got protocol.WorkspaceFilesGeneration
		if json.Unmarshal(raw, &msg) != nil || json.Unmarshal(msg.Payload, &got) != nil || got != g {
			t.Fatalf("generation missing in %s", raw)
		}
	}
	ch, _ := newWorkspaceFilesTestChannel(fakeWorkspaceFilesDirectory())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &workspaceFilesPending{id: "req", runtimeID: "runtime", resourceID: "resource", generation: g, ctx: ctx, cancel: cancel}
	ch.pending[p.id] = p
	ch.cancelPendingForRuntime(p.id, "other", g)
	ch.cancelPendingForRuntime(p.id, p.runtimeID, protocol.WorkspaceFilesGeneration{ConnectionEpoch: 42, RelaySeq: 72})
	ch.cancelPendingForRuntime(p.id, p.runtimeID, protocol.WorkspaceFilesGeneration{})
	if ctx.Err() != nil {
		t.Fatal("foreign or old generation canceled request")
	}
	ch.cancelPendingForRuntime(p.id, p.runtimeID, g)
	if ctx.Err() == nil || len(ch.pending) != 0 {
		t.Fatal("matching generation did not cancel")
	}
}
