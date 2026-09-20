package handler

import (
	"context"
	"testing"
)

// TestAuthorizeTerminalScopeRejectsBadInput pins the fail-closed branches
// that never touch the database: missing identity fields and malformed
// runtime ids deny the terminal subscription with not_found.
func TestAuthorizeTerminalScopeRejectsBadInput(t *testing.T) {
	h := &Handler{}
	cases := []struct {
		name       string
		userID     string
		workspace  string
		runtimeID  string
		wantReason string
	}{
		{"missing user", "", "ws", "6f9619ff-8b86-d011-b42d-00c04fc964ff", "not_found"},
		{"missing workspace", "6f9619ff-8b86-d011-b42d-00c04fc964ff", "", "6f9619ff-8b86-d011-b42d-00c04fc964ff", "not_found"},
		{"missing runtime", "6f9619ff-8b86-d011-b42d-00c04fc964ff", "ws", "", "not_found"},
		{"malformed runtime", "6f9619ff-8b86-d011-b42d-00c04fc964ff", "ws", "not-a-uuid", "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason, err := h.AuthorizeTerminalScope(context.Background(), tc.userID, tc.workspace, tc.runtimeID)
			if ok || err != nil || reason != tc.wantReason {
				t.Fatalf("got ok=%v reason=%q err=%v, want denied %q without error", ok, reason, err, tc.wantReason)
			}
		})
	}
}
