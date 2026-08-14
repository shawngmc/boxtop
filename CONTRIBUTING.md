# Contributing to boxtop

Feedback and contributions are welcome — this project is being used as an
entry point into Go, so suggestions on idioms and structure are especially
appreciated.

## Building

```sh
go mod tidy
go build -o boxtop .
./boxtop 1    # 1-second refresh
```

This plain build keeps the symbol table and debug info, which is what you
want for development. For a release-style binary that's roughly a third
smaller, strip those out (this is what the release workflow does):

```sh
go build -trimpath -ldflags="-s -w" -o boxtop .
```

`-s` drops the symbol table and `-w` drops DWARF debug info, so the result
isn't debuggable — fine for distribution, not for local hacking.

## Testing

```sh
go test ./...
```

## Cutting a pre-release

Releases are built and published automatically by the
[release workflow](.github/workflows/release.yml). It runs the tests,
cross-compiles Linux `amd64` and `arm64` binaries, and publishes a GitHub
pre-release with auto-generated notes.

To trigger it, push a tag matching `v*`:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The workflow attaches `boxtop-linux-amd64` and `boxtop-linux-arm64` to the
pre-release. To test the build without publishing, use the manual
**Run workflow** button (`workflow_dispatch`) on the Actions tab — it runs
the tests and build but skips the release step.

> The workflow needs `contents: write` permission (set in the workflow
> file). If publishing fails, check **Settings → Actions → General →
> Workflow permissions**.
