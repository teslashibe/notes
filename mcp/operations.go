package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"github.com/teslashibe/notes"
)

// Client is the existing Notes connector surface. Callers supply authorization.
type Client interface {
	List(context.Context) ([]notes.Note, error)
	Get(context.Context, string) (notes.Note, error)
	Checklist(context.Context, string) ([]notes.ChecklistItem, error)
	Create(context.Context, string, string) (notes.Note, error)
	Share(context.Context, string, []string) error
	AddChecklistItem(context.Context, string, string) ([]notes.ChecklistItem, error)
	EditChecklistItem(context.Context, string, string, string) ([]notes.ChecklistItem, error)
	SetChecked(context.Context, string, string, bool) ([]notes.ChecklistItem, error)
	MoveToRecentlyDeleted(context.Context, string) error
}

// NormalizeName conservatively folds case and whitespace, preserving punctuation.
func NormalizeName(name string) string {
	fields := strings.Fields(name)
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
		if NormalizeName(item.Text) == NormalizeName(text) {
			matches = append(matches, item)
		}
	}
	if len(matches) != 1 {
		return notes.ChecklistItem{}, false, len(matches) > 1
	}
	return matches[0], true, false
}

// Read inspects an already authorized ID, preserving partial-read availability.
func Read(ctx context.Context, client Client, out Outcome) Outcome {
	noteID := out.NoteID
	n, readErr := client.Get(ctx, noteID)
	bodyOK := readErr == nil && n.ID == noteID && !n.PasswordProtected
	if bodyOK {
		out = Metadata(n)
		out.Status = "completed"
		out.Body = &n.Body
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

// EditPayload is the existing internal single-item edit representation.
type EditPayload struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// ChangeItem validates current checklist state, then invokes claim immediately
// before an effect. The application owns the durable claim and completion journal.
// attempted is false for precondition failures and no-ops, which need no effect completion.
func ChangeItem(ctx context.Context, client Client, out *Outcome, action, text string, claim func() error) (attempted bool, err error) {
	noteID := out.NoteID
	current, e := client.Checklist(ctx, noteID)
	if e != nil {
		out.Fail("Could not read checklist; no note was changed", e)
		return false, nil
	}
	var edit EditPayload
	if action == "edit_note_item" {
		if e = json.Unmarshal([]byte(text), &edit); e != nil {
			out.Fail("Invalid edit payload", e)
			return false, nil
		}
		text = edit.OldText
	}
	out.Text = text
	existing, found, ambiguous := checklistMatch(current, text)
	if ambiguous {
		out.Fail("Multiple checklist items match; no note was changed", nil)
		return false, nil
	}
	if action == "add_note_item" && found {
		out.Status = "unchanged"
		out.Checked = &existing.Checked
		if existing.Checked {
			out.Message = "already_completed"
		} else {
			out.Message = "already_exists"
		}
		return false, nil
	}
	if action != "add_note_item" && (!found || existing.Text != text) {
		out.Fail("No unique checklist item matches; no note was changed", nil)
		return false, nil
	}
	if action == "edit_note_item" && edit.NewText == existing.Text {
		out.Status = "unchanged"
		out.Checked = &existing.Checked
		return false, nil
	}
	checked := action == "check_note_item"
	if (action == "check_note_item" || action == "uncheck_note_item") && existing.Checked == checked {
		out.Status = "unchanged"
		out.Checked = &existing.Checked
		return false, nil
	}
	if err = claim(); err != nil {
		return false, err
	}
	switch action {
	case "add_note_item":
		out.Checklist, e = client.AddChecklistItem(ctx, noteID, text)
	case "edit_note_item":
		out.Checklist, e = client.EditChecklistItem(ctx, noteID, existing.Text, edit.NewText)
		out.Text = edit.NewText
		out.Checked = &existing.Checked
	case "check_note_item", "uncheck_note_item":
		out.Checklist, e = client.SetChecked(ctx, noteID, existing.Text, checked)
		out.Checked = &checked
	default:
		e = errors.New("unknown Notes operation")
	}
	return true, e
}
