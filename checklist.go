package notes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ChecklistItem is a native Notes checklist paragraph, not a Unicode marker.
// Text must uniquely identify an item when changing its checked state.
type ChecklistItem struct {
	Text    string `json:"text"`
	Checked bool   `json:"checked"`
}

type nativeRequest struct {
	Operation    string   `json:"operation"`
	ID           string   `json:"id"`
	Text         string   `json:"text,omitempty"`
	Replacement  string   `json:"replacement,omitempty"`
	Participants []string `json:"participants,omitempty"`
	Checked      *bool    `json:"checked,omitempty"`
}

type nativeResponse struct {
	Link         string          `json:"link"`
	ID           string          `json:"id"`
	Items        []ChecklistItem `json:"items"`
	Error        string          `json:"error"`
	Verified     bool            `json:"verified"`
	Participants []string        `json:"participants"`
	Uncertain    bool            `json:"uncertain"`
	Deleted      bool            `json:"deleted"`
}

// Checklist reads actual native checked state using the installed native helper.
func (c Client) Checklist(ctx context.Context, id string) ([]ChecklistItem, error) {
	return c.native(ctx, nativeRequest{Operation: "checklist", ID: id})
}

// AddChecklistItem appends one native unchecked item without replacing the body.
// An existing exact item is rejected, rather than silently duplicated.
func (c Client) AddChecklistItem(ctx context.Context, id, text string) ([]ChecklistItem, error) {
	return c.native(ctx, nativeRequest{Operation: "add_checklist_item", ID: id, Text: text})
}

// SetChecked changes one exact, unique native item; already-correct state is a no-op.
func (c Client) SetChecked(ctx context.Context, id, text string, checked bool) ([]ChecklistItem, error) {
	return c.native(ctx, nativeRequest{Operation: "set_checked", ID: id, Text: text, Checked: &checked})
}

// EditChecklistItem replaces the text of one exact, unique native checklist
// item while preserving its checked state and native checklist formatting.
func (c Client) EditChecklistItem(ctx context.Context, id, text, replacement string) ([]ChecklistItem, error) {
	return c.native(ctx, nativeRequest{Operation: "edit_checklist_item", ID: id, Text: text, Replacement: replacement})
}

// MoveToRecentlyDeleted moves the exact note to Notes' recoverable Recently
// Deleted folder and verifies that it is no longer active. It never permanently deletes.
func (c Client) MoveToRecentlyDeleted(ctx context.Context, id string) error {
	result, err := c.nativeCall(ctx, nativeRequest{Operation: "move_to_recently_deleted", ID: id})
	if err == nil && !result.Deleted {
		return &OperationError{Operation: "move_to_recently_deleted", Uncertain: true, Err: errors.New("native helper did not verify deleted state")}
	}
	return err
}

func (c Client) native(ctx context.Context, req nativeRequest) ([]ChecklistItem, error) {
	result, err := c.nativeCall(ctx, req)
	return result.Items, err
}

// Share invites exactly the application-authorized participants and verifies the
// persisted collaboration. A failure may leave a shared note; never blindly retry.
func (c Client) Share(ctx context.Context, id string, participants []string) error {
	_, err := c.nativeCall(ctx, nativeRequest{Operation: "share", ID: id, Participants: participants})
	return err
}

// VerifyParticipants verifies persisted members without adding invitations.
func (c Client) VerifyParticipants(ctx context.Context, id string, participants []string) error {
	_, err := c.nativeCall(ctx, nativeRequest{Operation: "participants", ID: id, Participants: participants})
	return err
}

// SharedLink verifies the exact participants and returns a freshly copied iCloud link.
// It never adds invitations, making it safe for recovery after uncertain sharing.
func (c Client) SharedLink(ctx context.Context, id string, participants []string) (string, error) {
	result, err := c.nativeCall(ctx, nativeRequest{Operation: "shared_link", ID: id, Participants: participants})
	return result.Link, err
}

func (c Client) nativeCall(ctx context.Context, req nativeRequest) (nativeResponse, error) {
	fail := func(err error, uncertain bool) (nativeResponse, error) {
		return nativeResponse{}, &OperationError{Operation: req.Operation, Uncertain: uncertain, Err: err}
	}
	if ctx == nil || c.Timeout < 0 || !validID(req.ID) {
		return fail(ErrInvalidInput, false)
	}
	sharing := req.Operation == "share" || req.Operation == "participants" || req.Operation == "shared_link"
	deleting := req.Operation == "move_to_recently_deleted"
	if sharing {
		if len(req.Participants) < 1 || len(req.Participants) > 2 || (len(req.Participants) == 2 && req.Participants[0] == req.Participants[1]) {
			return fail(ErrInvalidInput, false)
		}
		for _, phone := range req.Participants {
			if len(phone) < 8 || len(phone) > 16 || phone[0] != '+' || strings.Trim(phone[1:], "0123456789") != "" {
				return fail(ErrInvalidInput, false)
			}
		}
	}
	if !sharing && !deleting && req.Operation != "checklist" && (strings.TrimSpace(req.Text) == "" || !validText(req.Text) || strings.ContainsAny(req.Text, "\r\n\u2028\u2029") || len(req.Text) > 4096) {
		return fail(ErrInvalidInput, false)
	}
	if req.Operation == "edit_checklist_item" && (strings.TrimSpace(req.Replacement) == "" || !validText(req.Replacement) || strings.ContainsAny(req.Replacement, "\r\n\u2028\u2029") || len(req.Replacement) > 4096) {
		return fail(ErrInvalidInput, false)
	}
	if c.NativeExecutable == "" {
		return fail(ErrUnsupported, false)
	}
	if !filepath.IsAbs(c.NativeExecutable) {
		return fail(ErrInvalidInput, false)
	}
	if err := ctx.Err(); err != nil {
		return fail(err, false)
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	if timeout > time.Minute {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	payload, err := json.Marshal(req)
	if err != nil {
		return fail(err, false)
	}
	cmd := exec.CommandContext(ctx, c.NativeExecutable)
	cmd.Stdin = strings.NewReader(string(payload))
	cmd.WaitDelay = time.Second
	stdout := &boundedBuffer{limit: 8 * 1024 * 1024, cancel: cancel}
	stderr := &boundedBuffer{limit: 16 * 1024, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return fail(err, false)
	}
	err = cmd.Wait()
	uncertain := req.Operation != "checklist" && req.Operation != "participants"
	if stdout.exceeded || stderr.exceeded {
		return fail(ErrOutputLimit, uncertain)
	}
	if ctx.Err() != nil {
		return fail(ctx.Err(), uncertain)
	}
	var result nativeResponse
	if decodeErr := json.Unmarshal(stdout.buffer.Bytes(), &result); decodeErr != nil {
		return fail(fmt.Errorf("native helper returned invalid JSON: %w", decodeErr), uncertain)
	}
	if result.Error != "" {
		return fail(errors.New(result.Error), uncertain && result.Uncertain)
	}
	if err != nil {
		return fail(fmt.Errorf("native helper: %w", err), uncertain)
	}
	if result.ID != req.ID {
		return fail(errors.New("native helper returned mismatched note"), uncertain)
	}
	if sharing {
		if !result.Verified || !slices.Equal(result.Participants, req.Participants) {
			return fail(errors.New("native helper did not verify exact participants"), uncertain)
		}
	} else if !deleting && result.Items == nil {
		return fail(errors.New("native helper returned missing items"), uncertain)
	}
	for _, item := range result.Items {
		if !validText(item.Text) {
			return fail(errors.New("native helper returned invalid item"), uncertain)
		}
	}
	if req.Operation == "shared_link" {
		u, err := url.Parse(result.Link)
		if err != nil || u.Scheme != "https" || u.Host != "www.icloud.com" || u.User != nil || !strings.HasPrefix(u.Path, "/notes/") || len(strings.TrimPrefix(u.Path, "/notes/")) == 0 {
			return fail(errors.New("verified iCloud Notes link missing"), false)
		}
	}
	return result, nil
}
