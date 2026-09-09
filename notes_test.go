package notes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("NOTES_NATIVE_TEST") == "1" {
		nativeTestProcess()
	}
	if os.Getenv("NOTES_HELPER") == "1" {
		source, err := io.ReadAll(os.Stdin)
		if err != nil || string(source) != script || len(os.Args) != 5 || os.Args[1] != "-l" || os.Args[2] != "JavaScript" || os.Args[3] != "-" {
			os.Exit(90)
		}
		var req request
		if json.Unmarshal([]byte(os.Args[4]), &req) != nil {
			os.Exit(91)
		}
		switch os.Getenv("NOTES_MODE") {
		case "fail":
			fmt.Fprint(os.Stderr, "automation denied")
			os.Exit(7)
		case "bad_json":
			fmt.Print("not json")
		case "missing":
			fmt.Print(`{}`)
		case "wrong_id":
			fmt.Print(`{"note":{"id":"wrong"}}`)
		case "not_found":
			fmt.Print(`{"error":{"code":"not_found","message":"missing"}}`)
		case "uncertain":
			fmt.Print(`{"error":{"code":"script_error","message":"readback failed"},"uncertain":true}`)
		case "timeout":
			time.Sleep(time.Minute)
		case "stdout_limit":
			fmt.Print(strings.Repeat("x", 9*1024*1024))
		case "stderr_limit":
			fmt.Fprint(os.Stderr, strings.Repeat("x", 20*1024))
		default:
			if req.Operation == "list" {
				fmt.Print(`{"notes":[{"id":"one","name":"same"},{"id":"two","name":"same"}]}`)
			} else {
				id := req.ID
				if id == "" {
					id = "created"
				}
				_ = json.NewEncoder(os.Stdout).Encode(response{Note: &Note{ID: id, Name: req.Title, Plaintext: req.Text, FolderID: req.FolderID}})
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func helper(t *testing.T, mode string) Client {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Client{executable: path, env: append(os.Environ(), "NOTES_HELPER=1", "NOTES_MODE="+mode)}
}

func TestLiteralPayloadAndIDs(t *testing.T) {
	c := helper(t, "")
	text := "\"'); throw new Error('injection'); //\n<div>&🙂\\\t"
	c.AccountID, c.FolderID = "account'\"", "folder'\""
	n, err := c.Create(context.Background(), "literal '\" <title>", text)
	if err != nil || n.Plaintext != text || n.FolderID != c.FolderID {
		t.Fatalf("literal input lost: %+v %v", n, err)
	}
	list, err := c.List(context.Background())
	if err != nil || len(list) != 2 || list[0].Name != list[1].Name {
		t.Fatalf("%+v %v", list, err)
	}
	n, err = c.Get(context.Background(), "two")
	if err != nil || n.ID != "two" {
		t.Fatalf("%+v %v", n, err)
	}
	n, err = c.Get(context.Background(), `opaque'\";throw new Error('bad')`)
	if err != nil || n.ID != `opaque'\";throw new Error('bad')` {
		t.Fatalf("%+v %v", n, err)
	}
}

func TestInvalidInput(t *testing.T) {
	c := helper(t, "")
	for _, id := range []string{"", " x", "x\n", "x\x00y", string([]byte{255}), strings.Repeat("x", 4097)} {
		if _, err := c.Get(context.Background(), id); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Get(%q): %v", id, err)
		}
	}
	for _, title := range []string{"", "  ", "multi\nline", "multi\u2028line", "nul\x00"} {
		if _, err := c.Create(context.Background(), title, "body"); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Create(%q): %v", title, err)
		}
	}
	if _, err := c.Create(context.Background(), "title", strings.Repeat("x", 65536)); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	c.FolderID = " bad"
	if _, err := c.Create(context.Background(), "title", "body"); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
}

func TestSubprocessFailures(t *testing.T) {
	for _, mode := range []string{"fail", "bad_json", "missing", "wrong_id", "not_found", "uncertain", "timeout", "stdout_limit", "stderr_limit"} {
		t.Run(mode, func(t *testing.T) {
			c := helper(t, mode)
			if mode == "timeout" {
				c.Timeout = 40 * time.Millisecond
			}
			_, err := c.Get(context.Background(), "id")
			if err == nil {
				t.Fatal("expected read error")
			}
			var op *OperationError
			if !errors.As(err, &op) || op.Uncertain {
				t.Fatalf("read uncertainty: %v", err)
			}
			if mode == "not_found" && !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			if mode == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if strings.HasSuffix(mode, "_limit") && !errors.Is(err, ErrOutputLimit) {
				t.Fatal(err)
			}
			if mode == "wrong_id" {
				return
			} // A fresh create may return any native ID.
			_, err = c.Create(context.Background(), "title", "text")
			if !errors.As(err, &op) || op.Uncertain != (mode != "not_found") {
				t.Fatalf("write uncertainty: %v", err)
			}
		})
	}
}

func TestNoWriteStarted(t *testing.T) {
	c := Client{executable: "/nonexistent/osascript"}
	_, err := c.Create(context.Background(), "title", "text")
	var op *OperationError
	if !errors.As(err, &op) || op.Uncertain {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = helper(t, "").Create(ctx, "title", "text")
	if !errors.Is(err, context.Canceled) || !errors.As(err, &op) || op.Uncertain {
		t.Fatal(err)
	}
}

func TestCapabilities(t *testing.T) {
	got := (Client{}).Capabilities()
	if !got.Read || !got.Create || got.Append || got.NativeChecklist || got.Sharing {
		t.Fatalf("%+v", got)
	}
}

// Execute the actual constant script in Apple's JavaScript runtime, replacing
// only Application with an in-memory model. Never opens Notes or creates a note.
func TestJXAContract(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires macOS JXA interpreter")
	}
	for _, req := range []request{
		{Operation: "list"},
		{Operation: "get", ID: "two"},
		{Operation: "get", ID: "absent"},
		{Operation: "get", ID: "deleted"},
		{Operation: "get", ID: "locked"},
		{Operation: "create", Title: "<title> '&", Text: "line 1\n<script>bad() & \" ' \\🙂", FolderID: "folder"},
		{Operation: "create", Title: "title", Text: "body"},
		{Operation: "create", Title: "title", Text: "body", AccountID: "account"},
		{Operation: "create", Title: "title", Text: "body", AccountID: "account", FolderID: "folder"},
		{Operation: "create", Title: "title", Text: "body", AccountID: "other", FolderID: "folder"},
	} {
		t.Run(req.Operation+req.ID+req.AccountID, func(t *testing.T) {
			payload, _ := json.Marshal(req)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-", string(payload))
			cmd.Stdin = strings.NewReader(strings.Replace(script, "var app = Application(\"Notes\");", fakeApplication+"\nvar app = Application(\"Notes\");", 1))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v", output, err)
			}
			var got response
			if err := json.Unmarshal(output, &got); err != nil {
				t.Fatalf("%s: %v", output, err)
			}
			switch {
			case req.ID == "absent" || req.ID == "deleted":
				if got.Error == nil || got.Error.Code != "not_found" {
					t.Fatalf("%s", output)
				}
			case req.ID == "locked":
				if got.Error == nil || got.Error.Code != "protected" {
					t.Fatalf("%s", output)
				}
			case req.AccountID == "other":
				if got.Error == nil || got.Error.Code != "invalid_input" || got.Uncertain {
					t.Fatalf("%s", output)
				}
			case req.Operation == "list":
				if got.Error != nil || len(got.Notes) != 3 || got.Notes[0].Body != "" {
					t.Fatalf("%s", output)
				}
				for _, note := range got.Notes {
					if note.ID == "deleted" {
						t.Fatal("deleted note returned in active list")
					}
				}
			case req.Operation == "get":
				if got.Note == nil || got.Note.ID != "two" || got.Note.Body != "<div>body two</div>" {
					t.Fatalf("%s", output)
				}
			default:
				if got.Error != nil || got.Note == nil || got.Note.ID != "new" {
					t.Fatalf("%s", output)
				}
				if strings.Contains(got.Note.Body, "<script>") || !strings.HasPrefix(got.Note.Body, "<h1>") {
					t.Fatalf("unsafe HTML: %s", output)
				}
				if req.Title == "<title> '&" && got.Note.Body != "<h1>&lt;title&gt; &#39;&amp;</h1><div>line 1<br>&lt;script&gt;bad() &amp; &quot; &#39; \\🙂</div>" {
					t.Fatalf("unexpected HTML: %s", got.Note.Body)
				}
			}
		})
	}
}

// TestLiveReadOnly exercises the native bridge only when explicitly requested.
// It never creates, updates, unlocks, or logs the contents of a note.
func TestLiveReadOnly(t *testing.T) {
	if os.Getenv("NOTES_LIVE_READONLY") != "1" {
		t.Skip("opt-in live read only")
	}
	c := Client{}
	ctx := context.Background()
	notes, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if n.PasswordProtected {
			continue
		}
		got, err := c.Get(ctx, n.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != n.ID {
			t.Fatal("ID mismatch")
		}
		return
	}
	t.Log("List succeeded; no unlocked note available for Get")
}

const fakeApplication = `
var Application = function(name) {
    if (name !== "Notes") throw new Error("wrong application");
    function prop(value) { return function() { return value; }; }
    function collection(items) {
        var f = prop(items);
        f.byId = function(id) { return items.filter(function(x) { return x.id() === id; })[0] || {exists: prop(false)}; };
        return f;
    }
    var account = {id:prop("account"), exists:prop(true)};
    var other = {id:prop("other"), exists:prop(true)};
    var folder = {id:prop("folder"), name:prop("Notes"), exists:prop(true), container:prop(account)};
    var deletedFolder = {id:prop("deleted-folder"), name:prop("Recently Deleted")};
    account.defaultFolder = prop(folder);
    function note(id, body, locked) {
        return {id:prop(id), name:prop("same"), exists:prop(true), container:prop(id === "deleted" ? deletedFolder : folder),
            creationDate:prop(new Date("2026-01-01T00:00:00Z")), modificationDate:prop(new Date("2026-01-02T00:00:00Z")),
            shared:prop(false), passwordProtected:prop(!!locked),
            body:function() { if (locked || id === "deleted") throw new Error("read inaccessible body"); return body; }, plaintext:prop("plain " + id)};
    }
    folder.notes = {push: function(n) { if (n.id() !== "new") throw new Error("existing note mutation"); }};
    return {
        notes: collection([note("one", "<div>body one</div>"), note("deleted", "deleted content"), note("two", "<div>body two</div>"), note("locked", "secret", true)]),
        accounts: collection([account, other]), folders: collection([folder]), defaultAccount:prop(account),
        Note:function(properties) { return note("new", properties.body); }
    };
};
`
