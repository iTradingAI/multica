package taskfailure

import "testing"

func TestTerminalErrorClassification(t *testing.T) {
	const marker = `; claude stderr: [claude-code:unrecognized_model] {"model":"glm-5.3","query_source":"sdk"}`
	for _, tc := range []struct {
		name, text string
		want       Reason
	}{
		{"bare cancellation", "execution cancelled", ReasonCancelled},
		{"cancellation with model warning", "execution cancelled" + marker, ReasonCancelled},
		{"cancelled process termination", "execution cancelled; claude stderr: process exited on signal", ReasonCancelled},
		{"startup rejection", "claude input/control protocol failed: write |1: file already closed" + marker, ReasonAgentProviderModelRejected},
		{"actual rate limit beats cancellation header", "execution cancelled; claude stderr: API Error: Request rejected (429)", ReasonAgentProviderCapacityOrRateLimit},
		{"model warning alone is not rejection", marker, ReasonAgentUnknown},
		{"unrelated prose", "tool request cancelled unexpectedly", ReasonAgentUnknown},
		{"unknown remains unknown", "unexplained failure", ReasonAgentUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.text); got != tc.want {
				t.Fatalf("Classify=%s, want %s", got, tc.want)
			}
			for _, legacy := range []string{"agent_error", string(ReasonAgentUnknown)} {
				want := tc.want
				if want == ReasonAgentUnknown && legacy == "agent_error" {
					want = Reason(legacy)
				}
				if got := NormalizeDaemonReason(legacy, tc.text); got != want {
					t.Fatalf("Normalize(%q)=%s, want %s", legacy, got, want)
				}
			}
		})
	}
	for _, tc := range []struct {
		text string
		want Reason
	}{
		{"prepare execution environment: mkdir: access denied", ReasonEnvironmentPrepareFailed},
		{"reuse execution environment: disk full", ReasonEnvironmentPrepareFailed},
	} {
		if got := NormalizeDaemonReason(string(ReasonAgentUnknown), tc.text); got != tc.want {
			t.Fatalf("host failure = %s, want %s", got, tc.want)
		}
	}
	if got := NormalizeDaemonReason(string(ReasonRuntimeRecovery), "execution cancelled"); got != ReasonRuntimeRecovery {
		t.Fatalf("explicit recovery overwritten: %s", got)
	}
}
