package notes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func nativeTestProcess() {
	var req nativeRequest
	if json.NewDecoder(os.Stdin).Decode(&req) != nil {
		os.Exit(90)
	}
	if path := os.Getenv("NOTES_NATIVE_CAPTURE"); path != "" {
		data, _ := json.Marshal(req)
		if os.WriteFile(path, data, 0600) != nil {
			os.Exit(91)
		}
	}
	switch os.Getenv("NOTES_NATIVE_MODE") {
	case "unverified":
		_ = json.NewEncoder(os.Stdout).Encode(nativeResponse{ID: req.ID, Items: []ChecklistItem{}})
	case "invalid":
		fmt.Print("not json")
	case "uncertain":
		fmt.Print(`{"error":"verification failed","uncertain":true}`)
		os.Exit(1)
	case "safe":
		fmt.Print(`{"error":"ambiguous item","uncertain":false}`)
		os.Exit(1)
	case "timeout":
		time.Sleep(time.Minute)
	case "wrong":
		fmt.Print(`{"id":"wrong","items":[]}`)
	case "members":
		_ = json.NewEncoder(os.Stdout).Encode(nativeResponse{ID: req.ID, Verified: true, Participants: []string{"+19999999999", "+18888888888"}})
	default:
		link := os.Getenv("NOTES_NATIVE_LINK")
		text := req.Text
		if req.Operation == "edit_checklist_item" {
			text = req.Replacement
		}
		_ = json.NewEncoder(os.Stdout).Encode(nativeResponse{ID: req.ID, Link: link, Items: []ChecklistItem{{Text: text, Checked: req.Checked != nil && *req.Checked}}, Verified: true, Participants: req.Participants, Deleted: req.Operation == "move_to_recently_deleted"})
	}
	os.Exit(0)
}

func TestSharingLinksPreserveExactRequestAndFailureDisposition(t *testing.T) {
	c := nativeHelper(t, "")
	participants := []string{"+15555501001", "+15555501002"}
	path := t.TempDir() + "/request.json"
	t.Setenv("NOTES_NATIVE_CAPTURE", path)
	for _, operation := range []string{"share", "shared_link"} {
		for _, link := range []string{"https://www.icloud.com/notes/fixture", "", "https://example.test/notes/fixture", "https://user@www.icloud.com/notes/fixture", "https://www.icloud.com/notes/", "https://www.icloud.com/notes/fixture\n"} {
			t.Setenv("NOTES_NATIVE_LINK", link)
			var got string
			var err error
			if operation == "share" {
				got, err = c.ShareWithLink(context.Background(), "exact-note", participants)
			} else {
				got, err = c.SharedLink(context.Background(), "exact-note", participants)
			}
			if link == "https://www.icloud.com/notes/fixture" {
				if err != nil || got != link {
					t.Fatalf("%s: %q %v", operation, got, err)
				}
			} else {
				var op *OperationError
				if !errors.As(err, &op) || op.Uncertain != (operation == "share") || got != "" {
					t.Fatalf("%s invalid link: %q %v", operation, got, err)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var req nativeRequest
			if err := json.Unmarshal(data, &req); err != nil || req.Operation != operation || req.ID != "exact-note" || strings.Join(req.Participants, ",") != strings.Join(participants, ",") {
				t.Fatalf("request changed: %+v %v", req, err)
			}
		}
	}
	// The legacy method remains compatible with helpers/callers that discard links.
	t.Setenv("NOTES_NATIVE_LINK", "")
	if err := c.Share(context.Background(), "exact-note", participants); err != nil {
		t.Fatal(err)
	}
}

func TestNativeTextEditContract(t *testing.T) {
	c := nativeHelper(t, "")
	path := t.TempDir() + "/request.json"
	t.Setenv("NOTES_NATIVE_CAPTURE", path)
	for _, replacement := range []string{"", "new\ntext ' \\"} {
		if err := c.EditText(context.Background(), "exact-note", "old\ntext 🐕", replacement); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var request nativeRequest
		if err := json.Unmarshal(data, &request); err != nil || request.ID != "exact-note" || request.Text != "old\ntext 🐕" || request.Replacement != replacement {
			t.Fatal(request, err)
		}
	}
	for _, bad := range []string{"\x00", "\uFFFC", strings.Repeat("x", 65537)} {
		if !errors.Is(c.EditText(context.Background(), "exact-note", "old", bad), ErrInvalidInput) {
			t.Fatal("invalid replacement accepted")
		}
	}
	for _, mode := range []string{"unverified", "uncertain", "wrong"} {
		t.Setenv("NOTES_NATIVE_MODE", mode)
		err := c.EditText(context.Background(), "exact-note", "old", "new")
		var op *OperationError
		if !errors.As(err, &op) || !op.Uncertain {
			t.Fatal(mode, err)
		}
	}
}
func nativeHelper(t *testing.T, mode string) Client {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOTES_NATIVE_TEST", "1")
	t.Setenv("NOTES_NATIVE_MODE", mode)
	return Client{NativeExecutable: path}
}
func TestNativeChecklistLiteral(t *testing.T) {
	c := nativeHelper(t, "")
	text := `Oat milk 🐕 "'; arbitrary literal`
	items, err := c.AddChecklistItem(context.Background(), "opaque-id", text)
	if err != nil || len(items) != 1 || items[0].Text != text || items[0].Checked {
		t.Fatal(items, err)
	}
	items, err = c.SetChecked(context.Background(), "opaque-id", text, true)
	if err != nil || !items[0].Checked {
		t.Fatal(items, err)
	}
	items, err = c.EditChecklistItem(context.Background(), "opaque-id", text, "Oat milk")
	if err != nil || len(items) != 1 || items[0].Text != "Oat milk" {
		t.Fatal(items, err)
	}
	if err = c.MoveToRecentlyDeleted(context.Background(), "opaque-id"); err != nil {
		t.Fatal(err)
	}
}

func TestNativeDeletionVerifiesFolderWithoutRequiringItBeforeDelete(t *testing.T) {
	data, err := os.ReadFile("native/main.swift")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if strings.Contains(source, "Recently Deleted folder missing or ambiguous") {
		t.Fatal("deletion still requires a pre-existing Recently Deleted folder")
	}
	for _, want := range []string{
		"note already in Recently Deleted",
		"app.delete(n)",
		"current.container().name()==='Recently Deleted'",
		"folder.notes.whose({id:{_equals:id}})()",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("native deletion lost %q verification", want)
		}
	}
	checked := strings.Index(source, "let checked = try execute(preflight)")
	wrote := strings.Index(source, "wrote = true")
	if checked < 0 || wrote < 0 || checked > wrote {
		t.Fatal("deletion marks uncertainty before its read-only preflight")
	}
}

func TestNativeValidation(t *testing.T) {
	c := nativeHelper(t, "")
	for _, text := range []string{"", "\n", "two\nlines", "two\u2028lines", strings.Repeat("x", 4097)} {
		_, err := c.AddChecklistItem(context.Background(), "id", text)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatal(text, err)
		}
		_, err = c.EditChecklistItem(context.Background(), "id", "old", text)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatal("replacement", text, err)
		}
	}
	for _, phones := range [][]string{nil, {"+15555501001"}, {"+15555501001", "+15555501001"}, {"+15555501001", "arbitrary"}} {
		if !errors.Is(c.Share(context.Background(), "id", phones), ErrInvalidInput) {
			t.Fatal(phones)
		}
	}
	_, err := (Client{}).Checklist(context.Background(), "id")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestNativeChecklistReportsLockedDesktopBeforeEditing(t *testing.T) {
	source, err := os.ReadFile("native/main.swift")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	locked := strings.Index(text, `bundleIdentifier == "com.apple.loginwindow"`)
	show := strings.Index(text, `let expected = try show(request.id)`)
	editor := strings.Index(text, `let editors = all.filter`)
	if locked < 0 || show < 0 || editor < 0 || locked > show || show > editor ||
		!strings.Contains(text, `try fail("Notes editor unavailable while the Mac is locked")`) {
		t.Fatal("native helper does not classify a locked desktop before activating Notes")
	}
	if !strings.Contains(text, `for attempt in 0..<15`) ||
		!strings.Contains(text, `if matched.count > 1 || attempt == 14 { try fail("ambiguous note editor") }`) ||
		!strings.Contains(text, `guard let current = try? content($0).0 else`) ||
		strings.Count(text, `try requireUnlockedDesktop()`) < 8 {
		t.Fatal("native helper does not bound cold editor discovery or recheck locks before mutation")
	}
}

func TestNativeUncertainty(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		uncertain bool
	}{{"invalid", true}, {"uncertain", true}, {"safe", false}, {"timeout", true}, {"wrong", true}} {
		t.Run(tc.mode, func(t *testing.T) {
			c := nativeHelper(t, tc.mode)
			if tc.mode == "timeout" {
				c.Timeout = 20 * time.Millisecond
			}
			_, err := c.SetChecked(context.Background(), "id", "item", true)
			var op *OperationError
			if !errors.As(err, &op) || op.Uncertain != tc.uncertain {
				t.Fatal(err)
			}
			_, err = c.Checklist(context.Background(), "id")
			if !errors.As(err, &op) || op.Uncertain {
				t.Fatal(err)
			}
			err = c.MoveToRecentlyDeleted(context.Background(), "id")
			if !errors.As(err, &op) || op.Uncertain != tc.uncertain {
				t.Fatal("delete", err)
			}
		})
	}
}
func TestNativeParticipants(t *testing.T) {
	phones := []string{"+15555501001", "+15555501002"}
	c := nativeHelper(t, "")
	if err := c.Share(context.Background(), "id", phones); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOTES_NATIVE_MODE", "members")
	if err := c.VerifyParticipants(context.Background(), "id", phones); err == nil {
		t.Fatal("accepted mismatched participants")
	}
}

func TestSharedLinkReadTimeoutCannotImplyUncertainSharing(t *testing.T) {
	c := nativeHelper(t, "timeout")
	c.Timeout = 50 * time.Millisecond
	_, err := c.SharedLink(context.Background(), "exact-note", []string{"+15555501001", "+15555501002"})
	var op *OperationError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &op) || op.Uncertain {
		t.Fatalf("read failure misclassified as a sharing effect: %v", err)
	}
}
