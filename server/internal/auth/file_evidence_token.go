package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

const FileEvidenceTokenPrefix = "mdt_fe1."

// FileEvidenceScope is daemon-only authority for one delivered claim. The
// complete token, including this envelope, is hashed in daemon_token. Parsing
// alone never authenticates it: changing any binding invalidates that hash.
// No source paths, claim contents or agent credential enter the envelope.
type FileEvidenceScope struct {
	WorkspaceID  string `json:"workspace_id"`
	DaemonID     string `json:"daemon_id"`
	TaskID       string `json:"task_id"`
	RuntimeID    string `json:"runtime_id"`
	DispatchedAt string `json:"dispatched_at"`
	ClaimDigest  string `json:"claim_digest"`
	ExpiresAt    int64  `json:"expires_at"`
}

func (s FileEvidenceScope) valid() bool {
	for _, value := range []string{s.WorkspaceID, s.TaskID, s.RuntimeID} {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || id.String() != value {
			return false
		}
	}
	dispatched, err := time.Parse(time.RFC3339Nano, s.DispatchedAt)
	digest, hashErr := hex.DecodeString(s.ClaimDigest)
	return s.DaemonID != "" && err == nil && !dispatched.IsZero() &&
		hashErr == nil && len(digest) == sha256.Size && s.ExpiresAt > time.Now().Unix()
}

func GenerateFileEvidenceToken(scope FileEvidenceScope) (string, error) {
	if !scope.valid() {
		return "", errors.New("invalid file evidence scope")
	}
	random, err := GenerateDaemonToken()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(scope)
	if err != nil {
		return "", err
	}
	return FileEvidenceTokenPrefix + base64.RawURLEncoding.EncodeToString(data) + "." + strings.TrimPrefix(random, "mdt_"), nil
}

// ParseFileEvidenceToken returns nil for legacy MDTs. A malformed or expired
// scoped token must fail closed, including when a daemon-auth cache is warm.
func ParseFileEvidenceToken(token string) (*FileEvidenceScope, error) {
	if !strings.HasPrefix(token, FileEvidenceTokenPrefix) {
		return nil, nil
	}
	parts := strings.Split(strings.TrimPrefix(token, FileEvidenceTokenPrefix), ".")
	if len(token) > 2048 || len(parts) != 2 {
		return nil, errors.New("invalid file evidence token")
	}
	random, err := hex.DecodeString(parts[1])
	if err != nil || len(random) != 20 {
		return nil, errors.New("invalid file evidence token")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("invalid file evidence token")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var scope FileEvidenceScope
	if decoder.Decode(&scope) != nil || decoder.Decode(new(any)) != io.EOF || !scope.valid() {
		return nil, errors.New("invalid file evidence token")
	}
	return &scope, nil
}

// FileEvidenceClaimDigest is stable across the JSONB round trip. UseNumber
// preserves integer generations rather than rounding them through float64.
func FileEvidenceClaimDigest(snapshot []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(snapshot))
	decoder.UseNumber()
	var claim map[string]any
	if decoder.Decode(&claim) != nil || claim == nil || decoder.Decode(new(any)) != io.EOF {
		return "", errors.New("invalid file evidence claim")
	}
	canonical, err := json.Marshal(claim)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:]), nil
}
