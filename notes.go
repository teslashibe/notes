// Package notes reads and creates Apple Notes using macOS JavaScript for Automation.
// It never accesses private databases or rewrites an existing note's body.
package notes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrUnsupported  = errors.New("notes: unsupported capability or platform")
	ErrInvalidInput = errors.New("notes: invalid input")
	ErrNotFound     = errors.New("notes: ID not found")
	ErrProtected    = errors.New("notes: note is password protected")
	ErrOutputLimit  = errors.New("notes: subprocess output limit exceeded")
)

// Capabilities describes configured operations, not whether macOS has granted
// Automation and Accessibility permission. Native operations require the helper.
type Capabilities struct {
	Read            bool
	Create          bool
	Append          bool
	NativeChecklist bool
	Sharing         bool
}

// Client's zero value uses Notes' default account and its default folder for
// creation. This is not necessarily iCloud. Explicit IDs never select by name.
// AccountID and FolderID affect Create only; List and Get cover all accounts.
// Requires macOS, Apple Notes, and user-granted Automation permission.
type Client struct {
	// NativeExecutable is the absolute path to the compiled native/main.swift helper.
	// An empty path disables native checklist operations.
	NativeExecutable string
	AccountID        string
	FolderID         string
	// Timeout defaults to 20 seconds and cannot exceed one minute.
	Timeout time.Duration

	// Private test seam; production always executes /usr/bin/osascript directly.
	executable string
	env        []string
}

// Note contains native metadata. Body (HTML) and Plaintext are populated only
// by Get and Create. List does not read content, including locked note content.
type Note struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	FolderID          string    `json:"folder_id"`
	Body              string    `json:"body,omitempty"`
	Plaintext         string    `json:"plaintext,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	ModifiedAt        time.Time `json:"modified_at"`
	Shared            bool      `json:"shared"`
	PasswordProtected bool      `json:"password_protected"`
}

// OperationError reports a failed call. If Uncertain is true, Create may have
// succeeded: reconcile using List/Get before deciding whether to retry. The
// client never retries a write automatically.
type OperationError struct {
	Operation string
	Uncertain bool
	Err       error
}

func (e *OperationError) Error() string {
	if e.Uncertain {
		return fmt.Sprintf("notes %s: outcome uncertain; do not blindly retry: %v", e.Operation, e.Err)
	}
	return fmt.Sprintf("notes %s: %v", e.Operation, e.Err)
}

func (e *OperationError) Unwrap() error { return e.Err }

func (c Client) Capabilities() Capabilities {
	return Capabilities{Read: true, Create: true, NativeChecklist: c.NativeExecutable != "", Sharing: c.NativeExecutable != ""}
}

// List returns metadata for all accessible notes, without fetching their bodies.
func (c Client) List(ctx context.Context) ([]Note, error) {
	r, err := c.call(ctx, request{Operation: "list"})
	return r.Notes, err
}

// Get reads one exact, opaque native note ID. Titles are never lookup keys.
// Password-protected notes are rejected without attempting to unlock them.
func (c Client) Get(ctx context.Context, id string) (Note, error) {
	if !validID(id) {
		return Note{}, ErrInvalidInput
	}
	r, err := c.call(ctx, request{Operation: "get", ID: id})
	if err != nil {
		return Note{}, err
	}
	return *r.Note, nil
}

// Create creates a NEW note from a single-line title and literal plain text.
// HTML is escaped; no existing body is ever assigned. Title becomes the first
// line of the new body because Notes derives its native name from that line.
// Payloads are limited to 64 KiB of encoded JSON for safe argv transport.
func (c Client) Create(ctx context.Context, title, text string) (Note, error) {
	if strings.TrimSpace(title) == "" || strings.ContainsAny(title, "\r\n\u2028\u2029") ||
		!validText(title) || !validText(text) ||
		(c.AccountID != "" && !validID(c.AccountID)) ||
		(c.FolderID != "" && !validID(c.FolderID)) {
		return Note{}, ErrInvalidInput
	}
	r, err := c.call(ctx, request{Operation: "create", Title: title, Text: text,
		AccountID: c.AccountID, FolderID: c.FolderID})
	if err != nil {
		return Note{}, err
	}
	return *r.Note, nil
}

func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }
func validID(s string) bool {
	return s != "" && len(s) <= 4096 && validText(s) && strings.TrimSpace(s) == s &&
		strings.IndexFunc(s, unicode.IsControl) < 0
}

type request struct {
	Operation string `json:"operation"`
	ID        string `json:"id,omitempty"`
	Title     string `json:"title,omitempty"`
	Text      string `json:"text,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	FolderID  string `json:"folder_id,omitempty"`
}

type response struct {
	Notes []Note `json:"notes"`
	Note  *Note  `json:"note"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Uncertain bool `json:"uncertain"`
}

func (c Client) call(ctx context.Context, req request) (response, error) {
	var result response
	fail := func(err error, uncertain bool) (response, error) {
		return response{}, &OperationError{Operation: req.Operation, Uncertain: uncertain, Err: err}
	}
	if runtime.GOOS != "darwin" && c.executable == "" {
		return fail(ErrUnsupported, false)
	}
	if ctx == nil || c.Timeout < 0 {
		return fail(ErrInvalidInput, false)
	}
	if err := ctx.Err(); err != nil {
		return fail(err, false)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return fail(err, false)
	}
	if len(payload) > 64*1024 {
		return fail(ErrInvalidInput, false)
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
	path := c.executable
	if path == "" {
		path = "/usr/bin/osascript"
	}
	cmd := exec.CommandContext(ctx, path, "-l", "JavaScript", "-", string(payload))
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = c.env
	cmd.WaitDelay = time.Second
	stdout := &boundedBuffer{limit: 8 * 1024 * 1024, cancel: cancel}
	stderr := &boundedBuffer{limit: 16 * 1024, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return fail(err, false)
	}
	err = cmd.Wait()
	uncertain := req.Operation == "create"
	if stdout.exceeded || stderr.exceeded {
		return fail(ErrOutputLimit, uncertain)
	}
	if ctx.Err() != nil {
		return fail(ctx.Err(), uncertain)
	}
	if err != nil {
		return fail(fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(stderr.buffer.String())), uncertain)
	}
	if err := json.Unmarshal(stdout.buffer.Bytes(), &result); err != nil {
		return fail(fmt.Errorf("invalid osascript JSON: %w", err), uncertain)
	}
	if result.Error != nil {
		var cause error
		switch result.Error.Code {
		case "not_found":
			cause = ErrNotFound
		case "protected":
			cause = ErrProtected
		case "invalid_input":
			cause = ErrInvalidInput
		default:
			cause = errors.New("Notes scripting failure")
		}
		return fail(fmt.Errorf("%w: %s", cause, result.Error.Message), uncertain && result.Uncertain)
	}
	if req.Operation == "list" {
		if result.Notes == nil {
			return fail(errors.New("missing notes in response"), false)
		}
		for _, n := range result.Notes {
			if !validID(n.ID) {
				return fail(errors.New("invalid note ID in response"), false)
			}
		}
	} else if result.Note == nil || !validID(result.Note.ID) ||
		(req.Operation == "get" && result.Note.ID != req.ID) {
		return fail(errors.New("missing or mismatched note ID in response"), uncertain)
	}
	return result, nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buffer.Len()
	if n > remaining {
		b.buffer.Write(p[:remaining])
		b.exceeded = true
		b.cancel()
		return n, nil
	}
	return b.buffer.Write(p)
}

// All input arrives as JSON argv data, never executable source. This script's
// only mutation is pushing a newly constructed Note into a resolved folder.
const script = `function run(argv) {
    var writing = false;
    try {
        var input = JSON.parse(argv[0]);
        var app = Application("Notes");
        function fail(code, message) {
            var e = new Error(message); e.code = code; throw e;
        }
        function exact(collection, id) {
            var object = collection.byId(id);
            if (!object.exists() || object.id() !== id) fail("not_found", "No object with the exact supplied ID");
            return object;
        }
        function metadata(note, content) {
            var value = {
                id: note.id(), name: note.name(), folder_id: note.container().id(),
                created_at: note.creationDate().toISOString(),
                modified_at: note.modificationDate().toISOString(),
                shared: note.shared(), password_protected: note.passwordProtected()
            };
            if (content) {
                if (value.password_protected) fail("protected", "Password-protected content is not read");
                value.body = note.body(); value.plaintext = note.plaintext();
            }
            return value;
        }
        function html(text) {
            return text.replace(/&/g, "&amp;").replace(/</g, "&lt;")
                .replace(/>/g, "&gt;").replace(/"/g, "&quot;")
                .replace(/'/g, "&#39;").replace(/\r\n|\r|\n|\u2028|\u2029/g, "<br>");
        }
        if (input.operation === "list") {
            return JSON.stringify({notes: app.notes().map(function (n) { return metadata(n, false); })});
        }
        if (input.operation === "get") {
            return JSON.stringify({note: metadata(exact(app.notes, input.id), true)});
        }
        if (input.operation !== "create") fail("invalid_input", "Unknown operation");
        var account = input.account_id ? exact(app.accounts, input.account_id) : null;
        var folder = input.folder_id ? exact(app.folders, input.folder_id) : (account || app.defaultAccount()).defaultFolder();
        if (input.account_id && input.folder_id) {
            var container = folder;
            var seen = {};
            while (container.id() !== account.id()) {
                var id = container.id();
                if (seen[id]) fail("invalid_input", "Folder ancestry cycle");
                seen[id] = true;
                if (app.accounts.byId(id).exists()) fail("invalid_input", "Folder is not in the supplied account");
                container = container.container();
            }
        }
        var body = "<div>" + html(input.title) + "</div><div>" + html(input.text || "") + "</div>";
        var note = app.Note({name: input.title, body: body});
        writing = true;
        folder.notes.push(note);
        return JSON.stringify({note: metadata(note, true)});
    } catch (e) {
        return JSON.stringify({error: {code: e.code || "script_error", message: String(e.message || e) + (e.errorNumber !== undefined ? " (" + e.errorNumber + ")" : "")}, uncertain: writing});
    }
}
`
