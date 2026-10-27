# AutoDNS CLI

`autodns` is the command-line client for AutoDNS. It authenticates against the AutoDNS authorization server with the OAuth device flow (or a panel-created `adns_k1_` API key), stores a local token, and calls the AutoDNS HTTP API for domain inventories, zone imports, and terminal-based record editing.

## Quick Start

With `autodns` installed, sign in and list the domains you can access:

```sh
autodns login
autodns list
```

Use `autodns list --json` for scripts, or `autodns list --help` for command options.

## Commands

```sh
autodns login
autodns list [--json] [--api-url URL]
autodns import [--dry-run] [--api-url URL] <zone-file>
autodns edit [--api-url URL] <domain-name>
autodns help
```

## Login

```sh
autodns login                      # device flow: visit the URL, enter the code, approve
autodns login --key adns_k1_...    # save a manually created API key instead
```

The device flow works on headless machines: no browser or localhost redirect is
needed on the machine running the CLI — you approve on any device that has one.
Access tokens are short-lived and refreshed automatically via a rotating
refresh token. Manual API keys never expire until revoked in the panel.

The OAuth client registration is cached at `~/.config/autodns/client.json`.

`login` saves a token at:

```text
~/.config/autodns/token.json
```

`list` shows owned and shared domains alphabetically with their stored record counts and totals:

```text
DOMAIN       RECORDS
example.com  12
example.net  5
example.org  8

3 domains / 25 records
```

`autodns list --json` prints only a JSON array, without table headers or totals:

```json
[{"Name":"example.com","RecordCount":12},{"Name":"example.net","RecordCount":5},{"Name":"example.org","RecordCount":8}]
```

Empty accounts produce `0 domains / 0 records` in table mode or `[]` in JSON mode. Counts come from a separate API request per domain and exclude synthesized authoritative SOA and NS records. If any request fails, the command exits nonzero without printing partial results. The listing does not report provider or synchronization state.

`import` treats a BIND zone file as desired state. The server computes the DNS diff and applies it unless `--dry-run` is set.

`edit` fetches the existing records for a domain, opens a temporary BIND-style zone file in `$VISUAL`, `$EDITOR`, or `vi`, shows the diff, and applies it after confirmation.

## Configuration

The CLI uses `https://app.autodns.sh` by default.

For `login`, `list`, `import`, and `edit`, override the server URL with
`AUTODNS_API_URL`, `--server` (login) or `--api-url` (other commands). The
command-line flag takes precedence over the environment variable:

```sh
AUTODNS_API_URL=https://dns.example.com autodns list
autodns list --api-url https://dns.example.com
autodns login --server https://dns.example.com
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

`make build` writes `./autodns` in this checkout. When working alongside the service repository, its `just build` command builds both the API and CLI, and `just build cli` builds only the CLI. Both copy the freshly built CLI to `./autodns` in the service checkout. The service expects this repository at `../autodns-cli`; set `AUTODNS_CLI_DIR` there to override the path.

## Implementation Notes

- Keep the CLI standard-library only unless a dependency removes meaningful complexity.
- Preserve the current command names and environment variables unless coordinating a matching API/service change.
- The API currently returns JSON with Go-style field names such as `Domain`, `Changes`, `Create`, and `Update`; the CLI structs mirror that wire shape.
- Avoid printing raw HTML or large non-JSON response bodies in terminal errors.
- Do not commit release artifacts from `dist/`.

## License

MIT; see [LICENSE](LICENSE).
