package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/filetouch"
)

func TestFileEvidenceConcurrentTaskCredentialsStayLocal(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		task := parts[4]
		want := "Bearer mdt_" + task
		if r.Header.Get("Authorization") != want {
			t.Errorf("task %s credential crossed into another request", task)
		}
		mu.Lock()
		seen[task]++
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer server.Close()
	client := NewClient(server.URL)
	client.SetToken("mul_control_plane")
	var wg sync.WaitGroup
	for _, task := range []string{"task-A", "task-B"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 12 {
				if err := client.RegisterTaskFileExecution(context.Background(), task, "mdt_"+task, filetouch.Execution{}); err != nil {
					t.Error(err)
				}
				if err := client.ReportTaskMessagesWithFileEvidence(context.Background(), task, "mdt_"+task, []TaskMessageData{{Type: "tool_use", Tool: "Write", Input: map[string]any{"file_path": "safe.txt"}}}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if client.token != "mul_control_plane" || seen["task-A"] != 24 || seen["task-B"] != 24 {
		t.Fatal("task credential replaced shared PAT or requests were lost")
	}
}

type fileEvidenceBackend struct {
	options agent.ExecOptions
}

func (b *fileEvidenceBackend) Execute(_ context.Context, _ string, opts agent.ExecOptions) (*agent.Session, error) {
	b.options = opts
	input := map[string]any{"file_path": "safe.txt"}
	message := agent.Message{Type: agent.MessageToolUse, Tool: "Write", CallID: "one-call", Input: input, PathIntegrity: filetouch.Origin("claude", "Write", runtime.GOOS, input, true)}
	stream := make(chan agent.Message, 1)
	stream <- message
	close(stream)
	result := make(chan agent.Result, 1)
	result <- agent.Result{Status: "completed"}
	return &agent.Session{Messages: stream, Result: result}, nil
}

func TestFileEvidenceExecutionRegistrationAndTerminalProof(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "expired"}[expire], func(t *testing.T) {
			credential := "mdt_task_private_credential"
			var registered string
			var reported []TaskMessageData
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if strings.HasSuffix(r.URL.Path, "/file-executions") {
					if r.Header.Get("Authorization") != "Bearer "+credential {
						t.Error("execution registration used shared PAT")
					}
					if expire {
						w.WriteHeader(401)
						return
					}
					var evidence filetouch.Execution
					if err := json.NewDecoder(r.Body).Decode(&evidence); err != nil {
						t.Error(err)
					}
					registered = evidence.ID
				} else if strings.HasSuffix(r.URL.Path, "/messages") {
					if r.Header.Get("Authorization") == "Bearer "+credential && expire {
						w.WriteHeader(401)
						return
					}
					want := "Bearer " + credential
					if expire {
						want = "Bearer mul_normal"
					}
					if r.Header.Get("Authorization") != want {
						t.Error("messages did not use task authority")
					}
					var body struct {
						Messages []TaskMessageData `json:"messages"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					reported = append(reported, body.Messages...)
				}
				w.WriteHeader(200)
			}))
			defer server.Close()
			client := NewClient(server.URL)
			client.SetToken("mul_normal")
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			daemon := &Daemon{client: client, logger: logger}
			backend := &fileEvidenceBackend{}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _, err := daemon.executeAndDrain(ctx, backend, "fixture", agent.ExecOptions{FilePathProvider: "claude", FileTouchExecution: &filetouch.Execution{Cwd: t.TempDir(), Platform: runtime.GOOS}}, logger, "task", "", new(atomic.Int32), credential)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(backend.options)
			if strings.Contains(string(encoded), credential) {
				t.Fatal("machine credential reached provider options")
			}
			if len(reported) != 1 {
				t.Fatalf("reported=%d", len(reported))
			}
			msg := reported[0]
			if expire {
				if registered != "" || msg.PathIntegrity != nil || msg.FileExecutionID != "" || msg.Input["file_path"] != filetouch.UnverifiedPath || backend.options.FilePathProvider != "" {
					t.Fatal("expired authority retained trusted file identity")
				}
			} else if registered == "" || msg.FileExecutionID != registered || msg.PathIntegrity == nil || msg.PathIntegrity.Slots[0].State != filetouch.Verified || msg.Input["file_path"] != "safe.txt" {
				t.Fatal("execution registration did not reach terminal proof")
			}
		})
	}
}

func TestFileEvidenceExpiryKeepsTranscriptWithoutProof(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.Header.Get("Authorization") == "Bearer mdt_expired" {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Authorization") != "Bearer mul_normal" {
			t.Error("fallback lost normal credential")
		}
		var body struct {
			Messages []TaskMessageData `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Messages) != 1 {
			t.Fatal("transcript was lost")
		}
		msg := body.Messages[0]
		if msg.PathIntegrity != nil || msg.FileExecutionID != "" || msg.SourceEventID != "" {
			t.Error("fallback carried trusted proof")
		}
		changes := msg.Input["changes"].([]any)
		if changes[0].(map[string]any)["path"] != filetouch.UnverifiedPath {
			t.Error("fallback retained path identity")
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	client := NewClient(server.URL)
	client.SetToken("mul_normal")
	original := []TaskMessageData{{Seq: 1, Type: "tool_use", Tool: "patch_apply", FileExecutionID: "execution", SourceEventID: "source", PathIntegrity: &filetouch.Integrity{ProofVersion: 1, Provider: "codex"}, Input: map[string]any{"changes": []any{map[string]any{"path": "safe.txt", "kind": "update"}}}, CreatedAt: time.Now()}}
	if err := client.ReportTaskMessagesWithFileEvidence(context.Background(), "task", "mdt_expired", original); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || original[0].FileExecutionID != "execution" || original[0].PathIntegrity == nil || original[0].Input["changes"].([]any)[0].(map[string]any)["path"] != "safe.txt" {
		t.Fatal("fallback mutated the shared batch or did not retry safely")
	}
}
