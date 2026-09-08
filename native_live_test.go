package notes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// This opt-in creates one private fixture note and moves only that note to
// Recently Deleted. It never shares a note, sends a message, or edits other notes.
// Run in the intended macOS GUI account using the established signed test runner.
func TestNativeNoteTextLifecycle(t *testing.T) {
	helper := os.Getenv("NOTES_LIVE_FIXTURE_HELPER")
	if helper == "" {
		t.Skip("explicit native Notes fixture opt-in required")
	}
	c := Client{NativeExecutable: helper, Timeout: 30 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	note, err := c.Create(ctx, fmt.Sprintf("agent-go verification %d", time.Now().UnixNano()), "Original paragraph.\nSecond paragraph.\nRepeated text.\nRepeated text.")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if err := c.MoveToRecentlyDeleted(cleanup, note.ID); err != nil {
			t.Errorf("fixture cleanup failed; retain the created fixture for review: %v", err)
		}
	}()
	if err := c.EditText(ctx, note.ID, "Original paragraph.", "Updated paragraph.\nAdditional line."); err != nil {
		t.Fatal(err)
	}
	after, err := c.Get(ctx, note.ID)
	if err != nil || !strings.Contains(after.Plaintext, "Updated paragraph.\nAdditional line.") || strings.Contains(after.Plaintext, "Original paragraph.") {
		t.Fatalf("exact text replacement was not visible: %v", err)
	}
	before := after.Plaintext
	err = c.EditText(ctx, note.ID, "Repeated text.", "Must not be written")
	var op *OperationError
	if !errors.As(err, &op) || op.Uncertain {
		t.Fatalf("ambiguous text was not rejected before writing: %v", err)
	}
	after, err = c.Get(ctx, note.ID)
	if err != nil || after.Plaintext != before {
		t.Fatalf("ambiguous edit changed the fixture: %v", err)
	}
	if _, err := c.AddChecklistItem(ctx, note.ID, "Fixture task"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetChecked(ctx, note.ID, "Fixture task", true); err != nil {
		t.Fatal(err)
	}
	err = c.EditText(ctx, note.ID, "Fixture task", "Must not replace a checklist item")
	if !errors.As(err, &op) || op.Uncertain {
		t.Fatalf("checklist text edit was not rejected before writing: %v", err)
	}
	items, err := c.EditChecklistItem(ctx, note.ID, "Fixture task", "Renamed fixture task")
	if err != nil || len(items) != 1 || items[0].Text != "Renamed fixture task" || !items[0].Checked {
		t.Fatalf("checklist state was not preserved: %v", err)
	}
	if err := c.EditText(ctx, note.ID, "Additional line.", ""); err != nil {
		t.Fatal(err)
	}
	items, err = c.Checklist(ctx, note.ID)
	if err != nil || len(items) != 1 || !items[0].Checked || items[0].Text != "Renamed fixture task" {
		t.Fatalf("paragraph removal disturbed the checklist: %v", err)
	}
}
