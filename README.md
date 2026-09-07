# notes

A Go connector for Apple Notes on macOS. It reads note metadata and content
and creates notes through JavaScript for Automation (JXA). An optional Swift
helper supports native checklists, collaboration, and recoverable deletion
through Notes UI automation.

The root package is independently usable. The `mcp` subpackage supplies tool
schemas and operation helpers; it is not an MCP server or standalone agent.

## Installation

Go 1.23 or newer on a currently supported patched release is required:

```sh
go get github.com/teslashibe/notes
```

Installing the Go module does not build, sign, install, or grant permissions to
the optional native helper.

## Compatibility

| Component | Linux | macOS without helper | macOS with helper |
| --- | --- | --- | --- |
| Package build and deterministic tests | Yes | Yes | Yes |
| `List`, `Get`, and `Create` | `ErrUnsupported` | Yes, with Automation permission | Yes, with Automation permission |
| Native checklist, sharing, link, and deletion operations | `ErrUnsupported` | Disabled | Best effort; requires Accessibility permission |

The helper imports AppKit and ApplicationServices, interacts with the
foreground Notes UI, and currently expects English UI labels. Its behavior can
change with macOS or Notes UI updates. Sharing and link operations may replace
clipboard contents. CI compiles the helper on the current GitHub-hosted macOS
runner but does not exercise Notes or prove end-to-end UI compatibility.

## Read-only example

This example accesses local Notes metadata but does not print note contents or
identities:

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

	all, err := (notes.Client{}).List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Accessible notes: %d\n", len(all))
}
```

`Client{}` uses Notes' default account and default folder for creation, which
need not be iCloud. `AccountID` and `FolderID` select the creation destination
only; `List` and `Get` cover all accounts. Use exact opaque IDs returned by the
API, never titles as lookup keys.

## Optional native helper

Build an unsigned helper on macOS using Apple's `swiftc`:

```sh
./scripts/build-native-helper
# Equivalent:
mkdir -p .build
swiftc native/main.swift -o .build/notes-native-helper
```

The script writes to `.build/notes-native-helper` by default (or to an explicit
first argument), refuses to write under `native/`, and performs no signing,
installation, permission grant, or Notes operation.

Signing and installation are separate operator-controlled steps. If your
deployment requires signing, sign with your established Apple identity and
verify it before installation; do not ad-hoc sign:

```sh
codesign --force --options runtime --sign "YOUR ESTABLISHED IDENTITY" .build/notes-native-helper
codesign --verify --strict --verbose=2 .build/notes-native-helper
install -d "$HOME/.local/bin"
install -m 0755 .build/notes-native-helper "$HOME/.local/bin/notes-native-helper"
```

Set `Client.NativeExecutable` to the installed absolute path. macOS prompts for
Automation or Accessibility access when relevant; grant those permissions
manually only after reviewing the binary and requested operation.

Native operations can foreground Notes and interact with its UI. Do not run
them concurrently with manual editing. `MoveToRecentlyDeleted` is recoverable,
not permanent deletion. `Capabilities` reports configuration, not permission
state or an operational guarantee.

## API constraints

- `List` returns metadata without fetching bodies, including for locked notes.
- `Get` rejects password-protected content without attempting to unlock it.
- `Create` creates a new note from a single-line title and escaped plain text;
  it never replaces an existing note body.
- There is no general append-body operation.
- Native mutations require exact IDs and preserve uncertain outcomes. Never
  blindly retry an `OperationError` whose `Uncertain` field is true.
- Calls default to 20 seconds and are capped at one minute. JXA request JSON is
  limited to 64 KiB, stdout to 8 MiB, and stderr to 16 KiB.

The `mcp` package provides schemas and helpers, not a complete dispatcher. A
host application remains responsible for authentication, authorization,
turn-binding, idempotent effect claims, participant configuration, and
deletion confirmation.

## Development

Run deterministic checks without live Notes access:

```sh
go build ./...
go vet ./...
NOTES_LIVE_READONLY=0 go test ./...
NOTES_LIVE_READONLY=0 go test -race ./...
```

Tests use fake subprocesses and clients. On macOS, a JXA contract test runs the
JavaScript interpreter against an in-memory replacement for Notes. It does not
open or write Notes. `TestLiveReadOnly` accesses real Notes only when explicitly
enabled with `NOTES_LIVE_READONLY=1`; never enable it in routine validation.

See [CONTRIBUTING.md](CONTRIBUTING.md), [SUPPORT.md](SUPPORT.md), and
[SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE).
