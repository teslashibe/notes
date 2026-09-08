package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/teslashibe/notes"
)

// Outcome is serialized in MCP text and the caller's existing operation journal.
type Outcome struct {
	Status             string                `json:"status"`
	NoteID             string                `json:"note_id,omitempty"`
	Title              string                `json:"title,omitempty"`
	FolderID           string                `json:"folder_id,omitempty"`
	ModifiedAt         *time.Time            `json:"modified_at,omitempty"`
	Message            string                `json:"message,omitempty"`
	IsError            bool                  `json:"is_error"`
	ReplayUnsafe       bool                  `json:"automatic_replay_unsafe,omitempty"`
	Notes              []Outcome             `json:"notes,omitempty"`
	Complete           *bool                 `json:"complete,omitempty"`
	Body               *string               `json:"body,omitempty"`
	Plaintext          *string               `json:"plaintext,omitempty"`
	BodyAvailable      *bool                 `json:"body_available,omitempty"`
	ChecklistAvailable *bool                 `json:"checklist_available,omitempty"`
	Checklist          []notes.ChecklistItem `json:"checklist,omitempty"`
	Text               string                `json:"text,omitempty"`
	Checked            *bool                 `json:"checked,omitempty"`
	Creation           string                `json:"creation,omitempty"`
	Sharing            string                `json:"sharing,omitempty"`
	Items              []Outcome             `json:"items,omitempty"`
	ExpiresAt          *time.Time            `json:"expires_at,omitempty"`
}

func (o Outcome) Encoded() string { data, _ := json.Marshal(o); return string(data) }

func Metadata(n notes.Note) Outcome {
	o := Outcome{NoteID: n.ID, Title: n.Name, FolderID: n.FolderID}
	if !n.ModifiedAt.IsZero() {
		o.ModifiedAt = &n.ModifiedAt
	}
	return o
}

// Fail preserves native uncertainty. The caller translates its journal errors.
func (o *Outcome) Fail(message string, err error) {
	o.Status = "failed"
	o.IsError = true
	o.Message = message
	var op *notes.OperationError
	if errors.As(err, &op) && op.Uncertain {
		o.Status = "uncertain"
		o.ReplayUnsafe = true
		o.Message += "; automatic replay is unsafe"
	}
}

// Reader is the existing connector read seam.
type Reader interface {
	Get(context.Context, string) (notes.Note, error)
	Checklist(context.Context, string) ([]notes.ChecklistItem, error)
}

// Read preserves independently available body/checklist portions.
func Read(ctx context.Context, client Reader, out Outcome) Outcome {
	noteID := out.NoteID
	n, readErr := client.Get(ctx, noteID)
	bodyOK := readErr == nil && n.ID == noteID && !n.PasswordProtected
	if bodyOK {
		out = Metadata(n)
		out.Status = "completed"
		out.Body = &n.Body
		out.Plaintext = &n.Plaintext
	}
	out.BodyAvailable = &bodyOK
	if !bodyOK {
		out.Fail("Could not read the selected note body", readErr)
	}
	items, checkErr := client.Checklist(ctx, noteID)
	checkOK := checkErr == nil
	out.ChecklistAvailable = &checkOK
	if checkOK {
		out.Checklist = items
	} else {
		out.Fail("Could not read checklist state", checkErr)
	}
	return out
}

func normalizeItem(text string) string {
	fields := strings.Fields(text)
	for i, field := range fields {
		fields[i] = strings.Map(func(r rune) rune {
			lowest := r
			for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
				if folded := unicode.ToLower(next); folded < lowest {
					lowest = folded
				}
			}
			return unicode.ToLower(lowest)
		}, field)
	}
	return strings.Join(fields, " ")
}

func checklistMatch(items []notes.ChecklistItem, text string) (notes.ChecklistItem, bool, bool) {
	var matches []notes.ChecklistItem
	for _, item := range items {
		if normalizeItem(item.Text) == normalizeItem(text) {
			matches = append(matches, item)
		}
	}
	if len(matches) != 1 {
		return notes.ChecklistItem{}, false, len(matches) > 1
	}
	return matches[0], true, false
}

// ItemChange holds one precondition-checked effect, never a batch or journal.
// The caller must authorize and claim the effect after PrepareItem and before Apply.
type ItemChange struct {
	action  string
	text    string
	newText string
	checked bool
}

// PrepareItem checks an observed checklist without making any change.
// A nil change means an unchanged result or a known precondition failure.
func PrepareItem(current []notes.ChecklistItem, action, text, newText string, out Outcome) (*ItemChange, Outcome) {
	out.Text = text
	existing, found, ambiguous := checklistMatch(current, text)
	if ambiguous {
		out.Fail("Multiple checklist items match; no note was changed", nil)
		return nil, out
	}
	if action == "add_note_item" && found {
		out.Status = "unchanged"
		out.Checked = &existing.Checked
		if existing.Checked {
			out.Message = "already_completed"
		} else {
			out.Message = "already_exists"
		}
		return nil, out
	}
	if action != "add_note_item" && (!found || existing.Text != text) {
		out.Fail("No unique checklist item matches; no note was changed", nil)
		return nil, out
	}
	if action == "edit_note_item" && newText == existing.Text {
		out.Status = "unchanged"
		out.Checked = &existing.Checked
		return nil, out
	}
	checked := action == "check_note_item"
	if (action == "check_note_item" || action == "uncheck_note_item") && existing.Checked == checked {
		out.Status = "unchanged"
		out.Checked = &existing.Checked
		return nil, out
	}
	if action == "edit_note_item" {
		checked = existing.Checked
	}
	return &ItemChange{action: action, text: text, newText: newText, checked: checked}, out
}

// ItemWriter reuses the native connector's exact-target and read-back checks.
type ItemWriter interface {
	AddChecklistItem(context.Context, string, string) ([]notes.ChecklistItem, error)
	EditChecklistItem(context.Context, string, string, string) ([]notes.ChecklistItem, error)
	SetChecked(context.Context, string, string, bool) ([]notes.ChecklistItem, error)
}

func (change ItemChange) Apply(ctx context.Context, client ItemWriter, out Outcome) (Outcome, error) {
	var err error
	switch change.action {
	case "add_note_item":
		out.Checklist, err = client.AddChecklistItem(ctx, out.NoteID, change.text)
	case "edit_note_item":
		out.Checklist, err = client.EditChecklistItem(ctx, out.NoteID, change.text, change.newText)
		out.Text = change.newText
		out.Checked = &change.checked
	case "check_note_item", "uncheck_note_item":
		out.Checklist, err = client.SetChecked(ctx, out.NoteID, change.text, change.checked)
		out.Checked = &change.checked
	default:
		err = errors.New("unknown Notes operation")
	}
	return out, err
}
