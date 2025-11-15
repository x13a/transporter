#!/usr/bin/env bash
set -euo pipefail

TRANSPORT="http"
SOCKS_ADDR="127.0.0.1:1080"
HTTP_ADDR="127.0.0.1:8080"
TCP_ADDR="127.0.0.1:9090"
FILE_DIR=""
PIPE_IN=""
PIPE_OUT=""
UDS_PATH="/tmp/transporter-dev.sock"
CURL_URL="https://ipinfo.io/ip"
CURL_UDP_URL="https://ip.me"
CURL_TIMEOUT=10

usage() {
cat <<USAGE
Usage: $0 [options]

Quickly start both server and client with a chosen transport. Defaults to HTTP.

Options:
  -t, --transport <name>
      Transport to use (http|tcp|file|uds|pipe|stdio). Default: http
  --socks <addr>
      SOCKS5 listen address for the client. Default: ${SOCKS_ADDR}
  --http <addr>
      HTTP transport bind address. Default: ${HTTP_ADDR}
  --tcp <addr>
      TCP transport bind address. Default: ${TCP_ADDR}
  --dir <path>
      Shared directory for file transport. Auto-temp if omitted.
  --pipe-in <path>
      Named pipe to read from (pipe transport). Auto-created if omitted.
  --pipe-out <path>
      Named pipe to write to (pipe transport). Auto-created if omitted.
  --uds <path>
      Unix domain socket path for uds transport. Default: ${UDS_PATH}
  --curl-url <url>
      URL used for TCP curl smoke test. Default: ${CURL_URL}
  --curl-udp-url <url>
      URL used for UDP curl smoke test. Default: ${CURL_UDP_URL}
  -h, --help
      Show this help message.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -t|--transport)
      TRANSPORT="$2"
      shift 2
      ;;
    --socks)
      SOCKS_ADDR="$2"
      shift 2
      ;;
    --http)
      HTTP_ADDR="$2"
      shift 2
      ;;
    --tcp)
      TCP_ADDR="$2"
      shift 2
      ;;
    --dir)
      FILE_DIR="$2"
      shift 2
      ;;
    --pipe-in)
      PIPE_IN="$2"
      shift 2
      ;;
    --pipe-out)
      PIPE_OUT="$2"
      shift 2
      ;;
    --uds)
      UDS_PATH="$2"
      shift 2
      ;;
    --curl-url)
      CURL_URL="$2"
      shift 2
      ;;
    --curl-udp-url)
      CURL_UDP_URL="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage
      exit 1
      ;;
  esac
done

declare -a SERVER_ARGS
declare -a CLIENT_ARGS
declare -a CLEANUP_PATHS=()
USE_STDIO_BRIDGE=0
STDIO_DIR=""
STDIO_S2C=""
STDIO_C2S=""

case "$TRANSPORT" in
  http)
    SERVER_ARGS=(
      -mode server
      -transport http
      -bind "$HTTP_ADDR"
      -verbose
    )
    CLIENT_ARGS=(
      -mode client
      -transport http
      -dest "http://$HTTP_ADDR"
      -socks "$SOCKS_ADDR"
      -verbose
    )
    ;;
  tcp)
    SERVER_ARGS=(
      -mode server
      -transport tcp
      -bind "$TCP_ADDR"
      -verbose
    )
    CLIENT_ARGS=(
      -mode client
      -transport tcp
      -dest "$TCP_ADDR"
      -socks "$SOCKS_ADDR"
      -verbose
    )
    ;;
  file)
    if [[ -z "$FILE_DIR" ]]; then
      FILE_DIR="$(mktemp -d)"
      CLEANUP_PATHS+=("$FILE_DIR")
    else
      mkdir -p "$FILE_DIR"
    fi
    SERVER_ARGS=(
      -mode server
      -transport file
      -dir "$FILE_DIR"
      -verbose
    )
    CLIENT_ARGS=(
      -mode client
      -transport file
      -dir "$FILE_DIR"
      -socks "$SOCKS_ADDR"
      -verbose
    )
    ;;
  uds)
    SERVER_ARGS=(
      -mode server
      -transport uds
      -path "$UDS_PATH"
      -verbose
    )
    CLIENT_ARGS=(
      -mode client
      -transport uds
      -path "$UDS_PATH"
      -socks "$SOCKS_ADDR"
      -verbose
    )
    CLEANUP_PATHS+=("$UDS_PATH")
    ;;
  pipe)
    if [[ -z "$PIPE_IN" || -z "$PIPE_OUT" ]]; then
      PIPE_TMP="$(mktemp -d)"
      PIPE_IN="${PIPE_TMP}/server_in"
      PIPE_OUT="${PIPE_TMP}/client_in"
      mkfifo "$PIPE_IN" "$PIPE_OUT"
      CLEANUP_PATHS+=("$PIPE_TMP")
    else
      [[ -p "$PIPE_IN" ]] || mkfifo "$PIPE_IN"
      [[ -p "$PIPE_OUT" ]] || mkfifo "$PIPE_OUT"
    fi
    SERVER_ARGS=(
      -mode server
      -transport pipe
      -in "$PIPE_IN"
      -out "$PIPE_OUT"
      -verbose
    )
    CLIENT_ARGS=(
      -mode client
      -transport pipe
      -in "$PIPE_OUT"
      -out "$PIPE_IN"
      -socks "$SOCKS_ADDR"
      -verbose
    )
    ;;
  stdio)
    USE_STDIO_BRIDGE=1
    STDIO_DIR="$(mktemp -d)"
    STDIO_S2C="${STDIO_DIR}/s2c"
    STDIO_C2S="${STDIO_DIR}/c2s"
    mkfifo "$STDIO_S2C" "$STDIO_C2S"
    CLEANUP_PATHS+=("$STDIO_DIR")
    SERVER_ARGS=(
      -mode server
      -transport stdio
      -verbose
    )
    CLIENT_ARGS=(
      -mode client
      -transport stdio
      -socks "$SOCKS_ADDR"
      -verbose
    )
    ;;
  *)
    echo "Unsupported transport: $TRANSPORT" >&2
    exit 1
    ;;
esac

start_stdio_proc() {
  local in_feed="$1"
  local out_feed="$2"
  shift 2
  (
    exec <"$in_feed"
    exec >"$out_feed"
    "$@"
  ) &
  echo $!
}

if [[ "$USE_STDIO_BRIDGE" -eq 1 ]]; then
  echo "[dev-run] Starting server over stdio bridge"
  SERVER_PID=$(start_stdio_proc "$STDIO_C2S" "$STDIO_S2C" go run . "${SERVER_ARGS[@]}")
else
  echo "[dev-run] Starting server: go run . ${SERVER_ARGS[*]}"
  go run . "${SERVER_ARGS[@]}" &
  SERVER_PID=$!
fi

cleanup() {
  echo "[dev-run] stopping..."
  kill "$SERVER_PID" >/dev/null 2>&1 || true
  if [[ -n "${CLIENT_PID:-}" ]]; then
    kill "$CLIENT_PID" >/dev/null 2>&1 || true
  fi
  wait "$SERVER_PID" "$CLIENT_PID" 2>/dev/null || true
  if [[ ${#CLEANUP_PATHS[@]} -gt 0 ]]; then
    for path in "${CLEANUP_PATHS[@]}"; do
      if [[ -n "$path" ]]; then
        rm -rf "$path"
      fi
    done
  fi
}

trap cleanup EXIT

sleep 0.5

if [[ "$USE_STDIO_BRIDGE" -eq 1 ]]; then
  echo "[dev-run] Starting client over stdio bridge"
  CLIENT_PID=$(start_stdio_proc "$STDIO_S2C" "$STDIO_C2S" go run . "${CLIENT_ARGS[@]}")
else
  echo "[dev-run] Starting client: go run . ${CLIENT_ARGS[*]}"
  go run . "${CLIENT_ARGS[@]}" &
  CLIENT_PID=$!
fi

sleep 1

echo -e "\n[dev-run] running curl TCP smoke test via socks5://${SOCKS_ADDR}"
TCP_OUTPUT=$(
  curl -sS \
    --socks5 "socks5://${SOCKS_ADDR}" \
    --max-time "${CURL_TIMEOUT}" \
    "${CURL_URL}" 2>&1 || true
)
printf '[dev-run] curl TCP response:\n%s\n\n' "$TCP_OUTPUT"

echo -e "[dev-run] running curl UDP test via socks5://${SOCKS_ADDR}"
UDP_OUTPUT=$(
  curl -sS \
    --socks5-hostname "socks5://${SOCKS_ADDR}" \
    --max-time "${CURL_TIMEOUT}" \
    "${CURL_UDP_URL}" 2>&1 || true
)
printf '[dev-run] curl UDP response:\n%s\n\n' "$UDP_OUTPUT"

echo "[dev-run] proxy running. Press Ctrl+C to stop."
wait "$CLIENT_PID"
