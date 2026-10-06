package agent

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

// Re-executes the test-created binary on Windows as well as Unix. The result
// and its usage arrive before cancellation, while process cleanup is pending.
func TestClaudeTerminalResultSurvivesCancellation(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"terminal_then_cancel", "terminal_then_control_error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			backend := &claudeBackend{cfg: Config{ExecutablePath: self, Env: map[string]string{"CLAUDE_FAKE_MODE": mode, "IS_SANDBOX": "1"}, Logger: slog.Default()}}
			session, err := backend.Execute(ctx, "prompt", ExecOptions{EnableTaskSupplement: mode == "terminal_then_control_error"})
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				for range session.Messages {
				}
			}()
			if session.TerminalObserved == nil {
				t.Fatal("missing authoritative terminal boundary")
			}
			for !session.TerminalObserved() {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(time.Millisecond):
				}
			}
			if mode == "terminal_then_cancel" {
				cancel()
			}
			select {
			case result, ok := <-session.Result:
				if !ok || result.Status != "completed" || result.Output != "delivered comment" || result.Error != "" {
					t.Fatalf("delivered result overwritten: %+v", result)
				}
				if usage := result.Usage["glm-5.3"]; usage.InputTokens != 9237 || usage.OutputTokens != 4062 {
					t.Fatalf("usage lost: %+v", result.Usage)
				}
				if mode == "terminal_then_control_error" && ctx.Err() != nil {
					t.Fatalf("external deadline drove cleanup instead of the control error: %v", ctx.Err())
				}
			case <-time.After(15 * time.Second):
				t.Fatal("terminal cleanup did not return")
			}
		})
	}
}
