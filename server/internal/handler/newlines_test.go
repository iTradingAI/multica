package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestIssueAndCommentWritesNormalizeNewlines(t *testing.T) {
	if testHandler == nil || dbfx == nil {
		t.Skip("database not available")
	}
	polluted := "中文\r\nsecond\r\r\nthird\rfourth\n\n`literal \\r\\n`"
	want := "中文\nsecond\nthird\nfourth\n\n`literal \\r\\n`"
	var issue IssueResponse
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "Newline regression", "description": polluted, "status": "todo", "priority": "medium",
	})
	testutil.Call(t, testHandler.CreateIssue, req).Want(http.StatusCreated).JSON(&issue)
	dbfx.Cleanup(t, `DELETE FROM issue WHERE id = $1`, issue.ID)
	if issue.Description == nil || *issue.Description != want {
		t.Fatalf("created issue API description = %v, want %q", issue.Description, want)
	}
	var stored string
	dbfx.QueryRow(t, `SELECT description FROM issue WHERE id = $1`, issue.ID).Scan(&stored)
	if stored != want {
		t.Fatalf("stored issue description = %q, want %q", stored, want)
	}

	req = withURLParam(newRequest("PATCH", "/api/issues/"+issue.ID, map[string]any{
		"description": polluted + "\r\r\nupdated",
	}), "id", issue.ID)
	testutil.Call(t, testHandler.UpdateIssue, req).Want(http.StatusOK).JSON(&issue)
	if issue.Description == nil || *issue.Description != want+"\nupdated" {
		t.Fatalf("updated issue API description = %v", issue.Description)
	}
	dbfx.QueryRow(t, `SELECT description FROM issue WHERE id = $1`, issue.ID).Scan(&stored)
	if stored != want+"\nupdated" {
		t.Fatalf("updated stored issue description = %q", stored)
	}

	var comment CommentResponse
	req = withURLParam(newRequest("POST", "/api/issues/"+issue.ID+"/comments", map[string]any{
		"content": polluted,
	}), "id", issue.ID)
	testutil.Call(t, testHandler.CreateComment, req).Want(http.StatusCreated).JSON(&comment)
	dbfx.Cleanup(t, `DELETE FROM comment WHERE id = $1`, comment.ID)
	if comment.Content != want {
		t.Fatalf("created comment API content = %q, want %q", comment.Content, want)
	}
	dbfx.QueryRow(t, `SELECT content FROM comment WHERE id = $1`, comment.ID).Scan(&stored)
	if stored != want {
		t.Fatalf("stored comment content = %q, want %q", stored, want)
	}
	// Rewriting the same document through Windows line endings must not create
	// a new revision once its stored content is already canonical.
	createdRevision := comment.Revision
	req = withURLParam(newRequest("PATCH", "/api/comments/"+comment.ID, map[string]any{
		"content": polluted, "content_base": want, "expected_revision": createdRevision,
	}), "commentId", comment.ID)
	testutil.Call(t, testHandler.UpdateComment, req).Want(http.StatusOK).JSON(&comment)
	if comment.Content != want || comment.Revision != createdRevision {
		t.Fatalf("canonical rewrite changed comment: content=%q revision=%d", comment.Content, comment.Revision)
	}
	// A normalized content_base must continue to pass strict edit checks.
	req = withURLParam(newRequest("PATCH", "/api/comments/"+comment.ID, map[string]any{
		"content": polluted + "\rupdated", "content_base": want, "expected_revision": comment.Revision,
	}), "commentId", comment.ID)
	testutil.Call(t, testHandler.UpdateComment, req).Want(http.StatusOK).JSON(&comment)
	if comment.Content != want+"\nupdated" {
		t.Fatalf("updated comment API content = %q", comment.Content)
	}
	dbfx.QueryRow(t, `SELECT content FROM comment WHERE id = $1`, comment.ID).Scan(&stored)
	if stored != want+"\nupdated" {
		t.Fatalf("updated stored comment content = %q", stored)
	}
}

func TestUpdateCommentNormalizesLegacyBodyWithRawBase(t *testing.T) {
	if testHandler == nil || dbfx == nil {
		t.Skip("database not available")
	}
	issueID := dbfx.Issue(t, "Legacy newline regression")
	legacy := "old\r\r\ncontent"
	commentID := dbfx.Comment(t, issueID, legacy)
	var revision int64
	dbfx.QueryRow(t, `SELECT revision FROM comment WHERE id = $1`, commentID).Scan(&revision)
	req := withURLParam(newRequest("PATCH", "/api/comments/"+commentID, map[string]any{
		"content": legacy + "\r\r\nupdated", "content_base": legacy, "expected_revision": revision,
	}), "commentId", commentID)
	var comment CommentResponse
	testutil.Call(t, testHandler.UpdateComment, req).Want(http.StatusOK).JSON(&comment)
	if comment.Content != "old\ncontent\nupdated" {
		t.Fatalf("legacy edit was not normalized: %q", comment.Content)
	}
}
