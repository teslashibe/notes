# Contributing

Issues and focused pull requests are welcome. Before proposing a change:

1. Describe the user-visible problem and its safety implications.
2. Keep platform automation exact-targeted and preserve uncertain outcomes.
3. Do not include note contents, account identifiers, phone numbers, or other
   personal data in reports or fixtures.

Run the deterministic checks from the repository root:

```sh
go test ./...
go vet ./...
go test -race ./...
```

On macOS, also compile the native helper without running it:

```sh
./scripts/build-native-helper
```

Never enable `NOTES_LIVE_READONLY=1` for routine validation. Tests and pull
requests must not invoke live Apple Notes, grant permissions, or publish signed
artifacts. By contributing, you agree that your contribution is licensed under
the repository's MIT License.
