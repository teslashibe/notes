# notes

A Go connector for Apple Notes on macOS. It reads note metadata/content and creates new notes through JavaScript for Automation (JXA). An optional Swift helper supports native checklists, collaboration, and recoverable deletion using Notes UI automation.

[agent-go](https://github.com/teslashibe/agent-go) uses this connector for its Notes integration. The root package is independently usable. The `mcp` subpackage supplies reusable tool schemas and operation helpers; it is not an MCP server or a standalone agent.

## Requirements and installation

- Go 1.23 or newer, as declared in `go.mod`.
- Live operations require macOS and Apple Notes. JXA uses `/usr/bin/osascript` and requires user-granted Automation permission.
- Native operations additionally require a separately compiled `native/main.swift` helper, Accessibility permission, and a usable Notes UI. The helper uses AppKit and ApplicationServices, foreground UI interaction, and English UI labels; compatibility depends on macOS/Notes UI behavior and locale. Sharing/link operations can change the clipboard.

With repository access and Git authentication configured, add the dependency from your application's Go module:

```sh
GOPRIVATE=github.com/teslashibe/notes go get github.com/teslashibe/notes
```

The repository is private; repository access is required, and this does not imply a public release. Include this module alongside any existing `GOPRIVATE` patterns. Installing the Go module does not build, install, sign, or grant permissions to the native helper.

## Read-only example

This example accesses local Notes metadata but does not print note contents or identities:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/teslashibe/notes"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := notes.Client{}
	all, err := client.List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Accessible notes: %d\n", len(all))
}
```

`Client{}` uses Notes' default account and default folder for creation, which need not be iCloud. `AccountID` and `FolderID` select the creation destination only; `List` and `Get` cover all accounts. Use exact opaque IDs returned by the API, never note titles as lookup keys.

## API and constraints

### JXA operations

- `List(ctx)` returns metadata without fetching bodies, including for locked notes.
- `Get(ctx, id)` returns HTML `Body` and `Plaintext` for an exact note ID. Password-protected content is rejected without an unlock attempt.
- `Create(ctx, title, text)` creates a new note from a single-line title and literal plain text. HTML is escaped, and the title becomes the first body line. It never replaces an existing note body.

JXA operations return `ErrUnsupported` outside macOS. The connector does not access private Notes databases. It has no general append-body operation: `Capabilities().Append` is false.

### Optional native operations

Set `Client.NativeExecutable` to the absolute path of an operator-provided build of `native/main.swift` to enable helper calls. An empty path disables them. The helper receives JSON on stdin; it is not a user-facing Go CLI. Building the Go packages below does not compile the Swift helper.

- `Checklist` reads actual native checked state, not text markers.
- `AddChecklistItem` appends an unchecked item and rejects an exact duplicate.
- `SetChecked` changes an exact unique item; already-correct state is a no-op.
- `EditChecklistItem` replaces an exact unique item's text while preserving checked state and checklist formatting.
- `MoveToRecentlyDeleted` moves a note to the recoverable Recently Deleted folder and verifies it is no longer active. It does not permanently delete a note.
- `Share` invites exactly the application-authorized participants and verifies persisted collaboration. The current API requires exactly two distinct phone-number strings in international `+`-prefixed form.
- `VerifyParticipants` checks persisted membership without inviting anyone. `SharedLink` verifies the same participants and returns a freshly copied iCloud Notes link without adding invitations.

`Capabilities` describes configuration, not a permissions check or a guarantee that a particular UI operation works. Native operations can foreground Notes and interact with its UI; do not run them concurrently with manual editing. No helper installation or signing procedure is automated by this repository.

### Errors, limits, and retries

Calls default to a 20-second timeout; larger configured timeouts are capped at one minute. JXA request payloads are limited to 64 KiB of encoded JSON. Subprocess stdout is limited to 8 MiB and stderr to 16 KiB.

Use `errors.Is` for sentinel errors such as `ErrInvalidInput`, `ErrNotFound`, `ErrProtected`, and `ErrUnsupported`, and `errors.As` for `*OperationError`. If `OperationError.Uncertain` is true, an effect may already have happened. Preserve the note ID and reconcile current state before retrying; the connector never retries a write automatically. Sharing failures can leave a note shared, and failures after creation can leave a newly created note.

## MCP contract and application responsibilities

Import `github.com/teslashibe/notes/mcp` for `Tools`, `Decode`, `Outcome`, `Read`, `PrepareItem`, and `ItemChange.Apply`. The catalog covers list/read, checklist item changes, shared-note creation, and a two-step deletion workflow.

These are schemas and helpers, not a complete tool dispatcher. The hosting application must provide transport, authenticate and authorize callers, bind operations to turns, journal/idempotently claim effects, configure participants, and enforce deletion confirmations and expiry. In particular, the low-level `MoveToRecentlyDeleted` method does not itself ask for confirmation. Tool arguments and note content must not supply authority. `PrepareItem` checks observed state; the caller must authorize and claim the effect before `Apply`.

## Development

From the repository root:

```sh
go build ./...
go vet ./...
NOTES_LIVE_READONLY=0 go test ./...
```

The default tests use fake subprocesses and clients. On macOS, the JXA contract test runs Apple's JavaScript interpreter against an in-memory replacement for Notes; it does not open Notes or write real notes. The separate `TestLiveReadOnly` test only accesses real Notes when `NOTES_LIVE_READONLY=1`; leave it disabled for device-independent validation. The Go tests do not establish native UI permissions or end-to-end helper compatibility.

## License

[MIT](LICENSE).
