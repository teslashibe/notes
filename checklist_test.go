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
	switch os.Getenv("NOTES_NATIVE_MODE") {
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
		_ = json.NewEncoder(os.Stdout).Encode(nativeResponse{ID: req.ID, Items: []ChecklistItem{{Text: req.Text, Checked: req.Checked != nil && *req.Checked}}, Verified: true, Participants: req.Participants})
	}
	os.Exit(0)
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
}
func TestNativeValidation(t *testing.T) {
	c := nativeHelper(t, "")
	for _, text := range []string{"", "\n", "two\nlines", "two\u2028lines", strings.Repeat("x", 4097)} {
		_, err := c.AddChecklistItem(context.Background(), "id", text)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatal(text, err)
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
