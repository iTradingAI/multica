package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/auth"
)

func TestFileEvidenceCredentialScopeAndCache(t *testing.T) {
	cache := auth.NewDaemonTokenCache(newRedisTestClient(t))
	digest, _ := auth.FileEvidenceClaimDigest([]byte(`{"proof_version":1}`))
	scope := auth.FileEvidenceScope{WorkspaceID: uuid.NewString(), DaemonID: uuid.NewString(), TaskID: uuid.NewString(), RuntimeID: uuid.NewString(), DispatchedAt: time.Now().UTC().Format(time.RFC3339Nano), ClaimDigest: digest, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	token, err := auth.GenerateFileEvidenceToken(scope)
	if err != nil {
		t.Fatal(err)
	}
	id := auth.DaemonTokenIdentity{WorkspaceID: scope.WorkspaceID, DaemonID: scope.DaemonID}
	cache.Set(context.Background(), auth.HashToken(token), id, time.Hour)
	base := "/api/daemon/tasks/" + scope.TaskID
	for _, tc := range []struct {
		name, method, path string
		status             int
	}{
		{"execution", "POST", base + "/file-executions", 204},
		{"messages", "POST", base + "/messages", 204},
		{"another-task", "POST", "/api/daemon/tasks/" + uuid.NewString() + "/messages", 403},
		{"claim", "POST", "/api/daemon/tasks/claim", 403},
		{"mcp", "POST", base + "/remote-mcp/resolve", 403},
		{"method", "GET", base + "/messages", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := FileEvidenceScopeFromContext(r.Context()); got == nil || *got != scope || DaemonIDFromContext(r.Context()) != scope.DaemonID {
					t.Fatal("authenticated scope missing")
				}
				if r.Header.Get("X-Agent-ID") != "" || r.Header.Get("X-Task-ID") != "" {
					t.Fatal("caller headers survived authentication")
				}
				w.WriteHeader(204)
			})
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("X-Agent-ID", "forged")
			r.Header.Set("X-Task-ID", "forged")
			w := httptest.NewRecorder()
			DaemonAuth(nil, nil, cache, nil)(next).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d", w.Code, tc.status)
			}
		})
	}
	for _, tc := range []struct {
		name, raw string
		identity  auth.DaemonTokenIdentity
		status    int
	}{
		{"binding-mismatch", token, auth.DaemonTokenIdentity{WorkspaceID: uuid.NewString(), DaemonID: scope.DaemonID}, 403},
		{"daemon-mismatch", token, auth.DaemonTokenIdentity{WorkspaceID: scope.WorkspaceID, DaemonID: uuid.NewString()}, 403},
		{"tampered-hash", token + "0", id, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh := auth.NewDaemonTokenCache(newRedisTestClient(t))
			// Only the original persisted hash authenticates; a caller cannot
			// rewrite metadata while retaining that authority.
			fresh.Set(context.Background(), auth.HashToken(token), tc.identity, time.Hour)
			r := httptest.NewRequest("POST", base+"/messages", nil)
			r.Header.Set("Authorization", "Bearer "+tc.raw)
			w := httptest.NewRecorder()
			DaemonAuth(nil, nil, fresh, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid credential reached handler") })).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d", w.Code, tc.status)
			}
		})
	}
	t.Run("expired-warm-cache", func(t *testing.T) {
		scope.ExpiresAt = time.Now().Add(-time.Minute).Unix()
		data, _ := json.Marshal(scope)
		expired := auth.FileEvidenceTokenPrefix + base64.RawURLEncoding.EncodeToString(data) + "." + strings.Repeat("a", 40)
		cache.Set(context.Background(), auth.HashToken(expired), id, time.Hour)
		r := httptest.NewRequest("POST", base+"/messages", nil)
		r.Header.Set("Authorization", "Bearer "+expired)
		w := httptest.NewRecorder()
		DaemonAuth(nil, nil, cache, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("expired scope reached handler") })).ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("expired status=%d", w.Code)
		}
	})
}

func TestDaemonPATIdentityIsNotMachineEvidence(t *testing.T) {
	cache := auth.NewPATCache(newRedisTestClient(t))
	owner := uuid.NewString()
	cache.Set(context.Background(), auth.HashToken("mul_owner"), owner, time.Hour)
	r := httptest.NewRequest("POST", "/api/daemon/tasks/claim", nil)
	r.Header.Set("Authorization", "Bearer mul_owner")
	r.Header.Set("X-User-ID", uuid.NewString())
	r.Header.Set("X-Daemon-ID", uuid.NewString())
	w := httptest.NewRecorder()
	DaemonAuth(nil, cache, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if DaemonUserIDFromContext(r.Context()) != owner || DaemonIDFromContext(r.Context()) != "" || FileEvidenceScopeFromContext(r.Context()) != nil {
			t.Fatal("PAT self-proof was promoted or owner identity was forged")
		}
		w.WriteHeader(204)
	})).ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("PAT status=%d", w.Code)
	}
}
