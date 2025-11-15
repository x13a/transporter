# transporter

Experimental toolkit for building a proxy where the client runs a local SOCKS5 endpoint,
encapsulates every TCP/UDP session into frames, and forwards them over pluggable transports.
The server consumes frames, opens real TCP connections or UDP sockets, and streams data in
both directions.

## Architecture

- `client` — exposes a built-in SOCKS5 server (CONNECT + UDP ASSOCIATE), assigns a unique
  `StreamID` per SOCKS session, and multiplexes frames over the selected transport.
- `server` — accepts frames, opens outbound TCP sockets or per-stream UDP listeners, and
  returns responses to the corresponding stream.
- `proto` — binary frame description (version, command, protocol, stream id, target
  host/port, payload, error string).
- `transport` — implementations that ship frames through different mediums: shared folder
  (`file`) and long-poll HTTP (`http`). Additional transports can register themselves via
  `transport.Register`.

### Frame layout

| Offset | Size | Description |
| --- | --- | --- |
| 0 | 1 | Version (`0x01`) |
| 1 | 1 | Command (`open`, `data`, `close`, `error`) |
| 2 | 1 | Protocol (`tcp` or `udp`) |
| 3 | 1 | Flags (reserved) |
| 4 | 4 | `StreamID` |
| 8 | 2 | Target address length |
| 10 | N | Target address (UTF-8) |
| 10+N | 2 | Target port |
| 12+N | 2 | Error string length |
| … | M | Error string (if present) |
| … | 4 | Payload length |
| … | P | Payload bytes |

The payload is limited to 1 MiB per frame to keep memory usage predictable.

## Usage

```bash
# HTTP transport
go run . -mode=server -transport=http -bind=127.0.0.1:8080
go run . -mode=client -transport=http -dest=http://127.0.0.1:8080 -socks=127.0.0.1:1080

# File transport
SHARED_DIR=/path/to/shared
go run . -mode=server -transport=file -dir=$SHARED_DIR
go run . -mode=client -transport=file -dir=$SHARED_DIR -socks=127.0.0.1:1080
```

Optional `-verbose` enables verbose logging for both client and server.

Transports are selected via `-transport`:

- `file` — both sides point `-dir=/path/to/folder` to the same shared directory (SMB,
  Dropbox, cloud drive, etc.). The transport creates `client_to_server` and
  `server_to_client` subfolders and writes each frame as a file. If `-dir` is omitted, a
  temporary folder is created per process.
- `http` — the server listens with `-bind=127.0.0.1:8080` (override explicitly to expose
  it), the client points `-dest=http://host:8080`. Frames are uploaded via POST `/upload`
  and downloaded via long-poll GET `/download`.
- `tcp` — raw TCP stream. The server accepts connections on `-bind`, the client dials
  `-dest`. Useful when a direct TCP tunnel exists (VPN/SSH port-forward).
- `uds` — Unix domain socket. Both roles share `-path=/tmp/transporter.sock`; useful for
  local-only setups or when exposing the proxy to other processes on the same host.
- `pipe` — two named pipes (FIFOs). Server reads from `-in` and writes to `-out`; client
  should invert them. Handy when you already have FIFO plumbing or want to integrate with
  other IPC tools.
- `stdio` — reads frames from `stdin` and writes to `stdout`. Useful for piping transports
  through SSH (`ssh remote ./server ... | ./client ...`), custom scripts, or embedding the
  proxy into other processes.

### Configuration file

Instead of passing long flag lists, provide a JSON config via `-config=/path/file.json` or
`TRANSPORTER_CONFIG=/path/file.json`. CLI flags still override config values. Example:

```json
{
  "mode": "client",
  "socks": "127.0.0.1:1080",
  "transport": "http",
  "verbose": true,
  "flags": {
    "dest": "https://example.org",
    "bind": "127.0.0.1:9090"
  }
}
```

Sample configs are available under `config/<transport>/client.json|server.json` for every
built-in transport.

## Testing

- `go test ./...` runs unit tests plus in-memory TCP and UDP integration tests.

### Make targets

- `make` / `make build` — build all packages and emit `bin/transporter`.
- `make test` — shorthand for `go test ./...`.
- `make live-test` — sequentially executes every live transport test (TCP and UDP
  variants) while automatically exporting all required `E2E_*` environment flags, so no
  manual setup is needed.
- `scripts/dev-run.sh` — convenience wrapper that launches both server and client locally
  (default HTTP transport). Pass `-t tcp|file|uds|pipe|stdio` (plus optional tuning flags)
  to quickly verify the proxy end-to-end; stop the script and it tears everything down
  and runs curl TCP/UDP smoke tests.

Each live transport test can also be run individually by setting the matching flag and
invoking `go test -run <TestName> ./...`:

- `TestLiveHTTPTransportProxy` — `E2E_HTTP_ENABLE=1` (optional `E2E_HTTP_TARGET`),
  verifies HTTP transport (TCP).
- `TestLiveHTTPTransportUDPProxy` — `E2E_HTTP_UDP_ENABLE=1`, verifies HTTP transport
  carrying UDP datagrams.
- `TestLiveFileTransportProxy` — `E2E_FILE_ENABLE=1` (optional `E2E_FILE_TARGET`),
  validates the shared folder transport (TCP).
- `TestLiveFileTransportUDPProxy` — `E2E_FILE_UDP_ENABLE=1`, validates shared folder
  transport (UDP).
- `TestLiveTCPTransportProxy` — `E2E_TCP_ENABLE=1` (optional `E2E_TCP_TARGET`), checks the
  raw TCP transport.
- `TestLiveTCPTransportUDPProxy` — `E2E_TCP_UDP_ENABLE=1`, checks UDP-through-TCP.
- `TestLiveUDSTransportProxy` — `E2E_UDS_ENABLE=1` (optional `E2E_UDS_TARGET`), covers UDS
  transport (Unix only).
- `TestLiveUDSTransportUDPProxy` — `E2E_UDS_UDP_ENABLE=1`, covers UDS transport for UDP
  datagrams.

Example:

```bash
export E2E_TCP_ENABLE=1
go test -run TestLiveTCPTransportProxy ./...

# Enable UDP HTTP transport test
export E2E_HTTP_UDP_ENABLE=1
go test -run TestLiveHTTPTransportUDPProxy ./...
```

## License

MIT
