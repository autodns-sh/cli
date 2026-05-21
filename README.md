# AutoDNS CLI

`autodns` is the command-line client for AutoDNS. It authenticates with GitHub OAuth device flow, stores a local bearer token, and calls the AutoDNS HTTP API for zone imports and terminal-based record editing.

## Commands

```sh
autodns login
autodns import [--dry-run] [--api-url URL] <zone-file>
autodns edit [--api-url URL] <domain-name>
autodns help
```

`login` saves a token at:

```text
~/.config/autodns/token.json
```

`import` treats a BIND zone file as desired state. The server computes the DNS diff and applies it unless `--dry-run` is set.

`edit` fetches the existing records for a domain, opens a temporary BIND-style zone file in `$VISUAL`, `$EDITOR`, or `vi`, shows the diff, and applies it after confirmation.

## Configuration

The CLI uses `https://app.autodns.sh` by default.

Override the API base URL with either:

```sh
AUTODNS_API_URL=https://app.autodns.sh autodns import zone.txt
autodns import --api-url https://app.autodns.sh zone.txt
```

The GitHub OAuth device-flow client ID is built into official releases. Custom builds can override it with:

```sh
AUTODNS_GITHUB_CLIENT_ID=... autodns login
autodns login --client-id ...
go build -ldflags="-X main.defaultClientID=..." ./cmd/autodns
```

## Releases

Tagged releases build static binaries for:

```text
autodns-linux-amd64
autodns-linux-arm64
autodns-darwin-amd64
autodns-darwin-arm64
autodns-windows-amd64.exe
```

Each release also includes `SHA256SUMS`.

## Development

This repository intentionally contains only the CLI. The AutoDNS service owns the API that this client calls.

Requirements:

```text
Go 1.25.6 or newer compatible Go 1.25 toolchain
make
```

Useful commands:

```sh
go test ./... -v -count=1
make build
make release
```

`make release` writes cross-platform binaries and checksums to `dist/`.

## Implementation Notes

- Keep the CLI standard-library only unless a dependency removes meaningful complexity.
- Preserve the current command names and environment variables unless coordinating a matching API/service change.
- The API currently returns JSON with Go-style field names such as `Domain`, `Changes`, `Create`, and `Update`; the CLI structs mirror that wire shape.
- Avoid printing raw HTML or large non-JSON response bodies in terminal errors.
- Do not commit release artifacts from `dist/`.
