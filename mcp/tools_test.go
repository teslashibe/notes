package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/teslashibe/notes"
)

func TestCatalogIsolationAndDecode(t *testing.T) {
	tools := Tools()
	if len(tools) != 10 {
		t.Fatal(len(tools))
	}
	tools[0]["inputSchema"].(map[string]any)["properties"].(map[string]any)["operation_id"].(map[string]any)["type"] = "number"
	if Tools()[0]["inputSchema"].(map[string]any)["properties"].(map[string]any)["operation_id"].(map[string]any)["type"] != "string" {
		t.Fatal("catalog mutation leaked")
	}
	for _, tool := range Tools() {
		name := tool["name"].(string)
		args := map[string]any{"operation_id": "op"}
		for _, key := range tool["inputSchema"].(map[string]any)["required"].([]string) {
			if key == "items" {
				args[key] = []string{"milk"}
			} else {
				args[key] = "value"
			}
		}
		raw, _ := json.Marshal(args)
		if _, err := Decode(name, raw); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		args["sender"] = "authority"
		raw, _ = json.Marshal(args)
		if _, err := Decode(name, raw); err == nil {
			t.Fatalf("%s accepted authority", name)
		}
	}
	for _, raw := range []string{`{"operation_id":"op","note_id":"id","items":[]}`, `{"operation_id":"op","note_id":"id","items":["x\ny"]}`, `{"operation_id":"op","note_id":null,"items":["x"]}`} {
		if _, err := Decode("add_note_items", json.RawMessage(raw)); err == nil {
			t.Fatal(raw)
		}
	}
}

func TestTextEditContract(t *testing.T) {
	for _, replacement := range []string{"", "new\nparagraph 🐕"} {
		raw, _ := json.Marshal(map[string]string{"operation_id": "edit", "note_id": "exact", "old_text": "old\nparagraph", "new_text": replacement})
		args, err := Decode("edit_note_text", raw)
		if err != nil || args.OldText != "old\nparagraph" || args.NewText != replacement {
			t.Fatal(args, err)
		}
	}
	for _, raw := range []string{
		`{"operation_id":"edit","note_id":"exact","old_text":"","new_text":"new"}`,
		`{"operation_id":"edit","note_id":"exact","old_text":"old","new_text":null}`,
		`{"operation_id":"edit","note_id":"exact","old_text":"old","new_text":"\ufffc"}`,
		`{"operation_id":"edit","note_id":"exact","old_text":"old"}`,
	} {
		if _, err := Decode("edit_note_text", json.RawMessage(raw)); err == nil {
			t.Fatal("accepted invalid text edit", raw)
		}
	}
}

func TestItemPreconditions(t *testing.T) {
	current := []notes.ChecklistItem{{Text: "Milk", Checked: true}}
	for _, tc := range []struct{ action, text, newText, status string }{
		{"add_note_item", " milk ", "", "unchanged"},
		{"check_note_item", "Milk", "", "unchanged"},
		{"edit_note_item", "Milk", "Milk", "unchanged"},
		{"edit_note_item", "milk", "Oat milk", "failed"},
		{"uncheck_note_item", "missing", "", "failed"},
	} {
		change, out := PrepareItem(current, tc.action, tc.text, tc.newText, Outcome{Status: "completed", NoteID: "id"})
		if change != nil || out.Status != tc.status {
			t.Fatalf("%+v: %+v", tc, out)
		}
	}
	change, out := PrepareItem(append(current, current[0]), "check_note_item", "Milk", "", Outcome{})
	if change != nil || !out.IsError {
		t.Fatal("ambiguous mutation")
	}
	change, out = PrepareItem(current, "edit_note_item", "Milk", "Oat milk", Outcome{NoteID: "id"})
	writer := &fixture{}
	out, err := change.Apply(context.Background(), writer, out)
	if err != nil || writer.text != "Milk" || writer.newText != "Oat milk" || out.Checked == nil || !*out.Checked {
		t.Fatalf("edit lost exact target/state: %+v %v", out, err)
	}
}

type fixture struct {
	text, newText     string
	bodyErr, checkErr error
}

func (f *fixture) Get(context.Context, string) (notes.Note, error) {
	return notes.Note{ID: "id", Body: "body"}, f.bodyErr
}
func (f *fixture) Checklist(context.Context, string) ([]notes.ChecklistItem, error) {
	return []notes.ChecklistItem{{Text: "Milk"}}, f.checkErr
}
func (f *fixture) AddChecklistItem(_ context.Context, _ string, text string) ([]notes.ChecklistItem, error) {
	f.text = text
	return nil, nil
}
func (f *fixture) EditChecklistItem(_ context.Context, _ string, text, newText string) ([]notes.ChecklistItem, error) {
	f.text = text
	f.newText = newText
	return nil, nil
}
func (f *fixture) SetChecked(_ context.Context, _ string, text string, _ bool) ([]notes.ChecklistItem, error) {
	f.text = text
	return nil, nil
}
func TestPartialReadsAndUncertainty(t *testing.T) {
	for _, bodyFails := range []bool{false, true} {
		f := &fixture{}
		if bodyFails {
			f.bodyErr = errors.New("body")
		} else {
			f.checkErr = errors.New("checklist")
		}
		out := Read(context.Background(), f, Outcome{Status: "completed", NoteID: "id"})
		if !out.IsError || *out.BodyAvailable == bodyFails || *out.ChecklistAvailable != bodyFails {
			t.Fatalf("%+v", out)
		}
	}
	out := Outcome{NoteID: "id"}
	out.Fail("write", &notes.OperationError{Uncertain: true, Err: errors.New("timeout")})
	if out.Status != "uncertain" || !out.ReplayUnsafe || !out.IsError {
		t.Fatal(out)
	}
	var decoded Outcome
	if err := json.Unmarshal([]byte(out.Encoded()), &decoded); err != nil || decoded.Status != out.Status || decoded.NoteID != out.NoteID || !decoded.ReplayUnsafe {
		t.Fatal("outcome roundtrip")
	}
}
