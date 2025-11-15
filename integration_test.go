package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"transporter/client"
	"transporter/proto"
	"transporter/server"
	"transporter/transport"
)

const defaultTestTarget = "https://ipinfo.io/ip"

func TestEndToEndMemoryTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientConn, serverConn := newMemPair()

	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(serverCtx, server.Config{
			Transport: serverConn,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				c1, c2 := net.Pipe()
				go func() {
					defer c2.Close()
					io.Copy(c2, c2)
				}()
				return c1, nil
			},
		})
	}()

	clientCtx, clientCancel := context.WithCancel(ctx)
	defer clientCancel()
	clientErr := make(chan error, 1)
	socksAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("alloc socks addr: %v", err)
	}
	go func() {
		clientErr <- client.Run(clientCtx, client.Config{
			SocksListen: socksAddr,
			Transport:   clientConn,
		})
	}()

	conn, err := dialSOCKSConn(socksAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial socks: %v", err)
	}
	defer conn.Close()

	const backendHost = "10.0.0.1"
	const backendPort = 8081

	if err := socks5Connect(conn, backendHost, backendPort); err != nil {
		t.Fatalf("socks handshake failed: %v", err)
	}

	payload := []byte("hello transporter")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}

	if string(buf) != string(payload) {
		t.Fatalf("unexpected echo: got %q want %q", buf, payload)
	}

	clientCancel()
	serverCancel()
	waitErr(t, clientErr, "client")
	waitErr(t, serverErr, "server")
}

func TestUDPProxyOverMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientConn, serverConn := newMemPair()

	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(serverCtx, server.Config{Transport: serverConn})
	}()

	udpTarget, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Skipf("cannot bind udp target: %v", err)
	}
	defer udpTarget.Close()
	go runUDPEcho(udpTarget)

	socksAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("alloc socks addr: %v", err)
	}

	clientCtx, clientCancel := context.WithCancel(ctx)
	defer clientCancel()
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run(clientCtx, client.Config{
			SocksListen: socksAddr,
			Transport:   clientConn,
		})
	}()

	payload := []byte("transporter-udp")
	localUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp client: %v", err)
	}
	defer localUDP.Close()

	udpConn, udpBind := startUDPAssociation(t, socksAddr, localUDP.LocalAddr().(*net.UDPAddr))
	defer udpConn.Close()

	request, err := buildSocksUDPPacket(udpTarget.LocalAddr().(*net.UDPAddr), payload)
	if err != nil {
		t.Fatalf("build udp request: %v", err)
	}
	if _, err := localUDP.WriteToUDP(request, udpBind); err != nil {
		t.Fatalf("send udp via socks: %v", err)
	}

	_ = localUDP.SetReadDeadline(time.Now().Add(2 * time.Second))
	respBuf := make([]byte, 64*1024)
	n, _, err := localUDP.ReadFromUDP(respBuf)
	if err != nil {
		t.Fatalf("read udp reply: %v", err)
	}

	host, port, data, err := parseSocksUDPPacket(respBuf[:n])
	if err != nil {
		t.Fatalf("parse udp reply: %v", err)
	}
	targetAddr := udpTarget.LocalAddr().(*net.UDPAddr)
	if host != targetAddr.IP.String() || port != targetAddr.Port {
		t.Fatalf("unexpected udp origin %s:%d", host, port)
	}
	expected := bytes.ToUpper(payload)
	if !bytes.Equal(data, expected) {
		t.Fatalf("unexpected udp payload: got %q want %q", data, expected)
	}

	cancel()
	waitErr(t, clientErr, "client")
	waitErr(t, serverErr, "server")
}

func TestLiveHTTPTransportProxy(t *testing.T) {
	if os.Getenv("E2E_HTTP_ENABLE") == "" {
		t.Skip("set E2E_HTTP_ENABLE=1 to enable live HTTP proxy test")
	}

	targetURL := os.Getenv("E2E_HTTP_TARGET")
	if targetURL == "" {
		targetURL = defaultTestTarget
	}

	httpAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("cannot allocate http listen port: %v", err)
	}

	ensureTransportFlags()
	if err := flag.CommandLine.Set("bind", httpAddr); err != nil {
		t.Fatalf("set bind: %v", err)
	}
	if err := flag.CommandLine.Set("dest", fmt.Sprintf("http://%s", httpAddr)); err != nil {
		t.Fatalf("set dest: %v", err)
	}

	sharedDir := t.TempDir()
	ensureTransportFlags()
	if err := flag.CommandLine.Set("dir", sharedDir); err != nil {
		t.Fatalf("set dir flag: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverConn, err := transport.Open(ctx, transport.RoleServer, "http")
	if err != nil {
		t.Fatalf("open server transport: %v", err)
	}
	defer serverConn.Close()

	clientConn, err := transport.Open(ctx, transport.RoleClient, "http")
	if err != nil {
		t.Fatalf("open client transport: %v", err)
	}
	defer clientConn.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(ctx, server.Config{
			Transport: serverConn,
		})
	}()

	socksAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("cannot allocate socks addr: %v", err)
	}

	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run(ctx, client.Config{
			SocksListen: socksAddr,
			Transport:   clientConn,
		})
	}()

	time.Sleep(300 * time.Millisecond)

	httpClient := newHTTPClientThroughSOCKS(t, socksAddr)
	resp, err := httpClient.Get(targetURL)
	if err != nil {
		t.Fatalf("http request via proxy failed: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, body)
	}
	if len(body) == 0 {
		t.Fatalf("empty body from %s", targetURL)
	}

	cancel()
	waitErr(t, clientErr, "client")
	waitErr(t, serverErr, "server")
}

func TestLiveHTTPTransportUDPProxy(t *testing.T) {
	if os.Getenv("E2E_HTTP_UDP_ENABLE") == "" {
		t.Skip("set E2E_HTTP_UDP_ENABLE=1 to enable live HTTP UDP test")
	}

	httpAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("cannot allocate http listen port: %v", err)
	}

	ensureTransportFlags()
	if err := flag.CommandLine.Set("bind", httpAddr); err != nil {
		t.Fatalf("set bind: %v", err)
	}
	if err := flag.CommandLine.Set("dest", fmt.Sprintf("http://%s", httpAddr)); err != nil {
		t.Fatalf("set dest: %v", err)
	}

	sharedDir := t.TempDir()
	ensureTransportFlags()
	if err := flag.CommandLine.Set("dir", sharedDir); err != nil {
		t.Fatalf("set dir flag: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverConn, err := transport.Open(ctx, transport.RoleServer, "http")
	if err != nil {
		t.Fatalf("open server transport: %v", err)
	}
	defer serverConn.Close()

	clientConn, err := transport.Open(ctx, transport.RoleClient, "http")
	if err != nil {
		t.Fatalf("open client transport: %v", err)
	}
	defer clientConn.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(ctx, server.Config{Transport: serverConn})
	}()

	socksAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("cannot allocate socks addr: %v", err)
	}

	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run(ctx, client.Config{
			SocksListen: socksAddr,
			Transport:   clientConn,
		})
	}()

	time.Sleep(300 * time.Millisecond)
	exerciseUDPProxy(t, socksAddr)

	cancel()
	waitErr(t, clientErr, "client")
	waitErr(t, serverErr, "server")
}

func TestLiveFileTransportProxy(t *testing.T) {
	if os.Getenv("E2E_FILE_ENABLE") == "" {
		t.Skip("set E2E_FILE_ENABLE=1 to enable live file transport test")
	}

	targetURL := os.Getenv("E2E_FILE_TARGET")
	if targetURL == "" {
		targetURL = defaultTestTarget
	}

	dir := t.TempDir()
	ensureTransportFlags()
	if err := flag.CommandLine.Set("dir", dir); err != nil {
		t.Fatalf("set dir flag: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverConn, err := transport.Open(ctx, transport.RoleServer, "file")
	if err != nil {
		t.Fatalf("open server transport: %v", err)
	}
	defer serverConn.Close()
	clientConn, err := transport.Open(ctx, transport.RoleClient, "file")
	if err != nil {
		t.Fatalf("open client transport: %v", err)
	}
	defer clientConn.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(ctx, server.Config{
			Transport: serverConn,
		})
	}()

	socksAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("cannot allocate socks addr: %v", err)
	}
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run(ctx, client.Config{
			SocksListen: socksAddr,
			Transport:   clientConn,
		})
	}()

	time.Sleep(300 * time.Millisecond)

	httpClient := newHTTPClientThroughSOCKS(t, socksAddr)
	resp, err := httpClient.Get(targetURL)
	if err != nil {
		t.Fatalf("http request via file transport failed: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, body)
	}
	if len(body) == 0 {
		t.Fatalf("empty body from %s", targetURL)
	}

	cancel()
	waitErr(t, clientErr, "client")
	waitErr(t, serverErr, "server")
}

func TestLiveFileTransportUDPProxy(t *testing.T) {
	if os.Getenv("E2E_FILE_UDP_ENABLE") == "" {
		t.Skip("set E2E_FILE_UDP_ENABLE=1 to enable live file UDP test")
	}

	dir := t.TempDir()
	ensureTransportFlags()
	if err := flag.CommandLine.Set("dir", dir); err != nil {
		t.Fatalf("set dir flag: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverConn, err := transport.Open(ctx, transport.RoleServer, "file")
	if err != nil {
		t.Fatalf("open server transport: %v", err)
	}
	defer serverConn.Close()
	clientConn, err := transport.Open(ctx, transport.RoleClient, "file")
	if err != nil {
		t.Fatalf("open client transport: %v", err)
	}
	defer clientConn.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(ctx, server.Config{Transport: serverConn})
	}()

	socksAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("cannot allocate socks addr: %v", err)
	}
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run(ctx, client.Config{
			SocksListen: socksAddr,
			Transport:   clientConn,
		})
	}()

	time.Sleep(300 * time.Millisecond)
	exerciseUDPProxy(t, socksAddr)

	cancel()
	waitErr(t, clientErr, "client")
	waitErr(t, serverErr, "server")
}

func TestLiveTCPTransportProxy(t *testing.T) {
	if os.Getenv("E2E_TCP_ENABLE") == "" {
		t.Skip("set E2E_TCP_ENABLE=1 to enable live TCP transport test")
	}

	targetURL := os.Getenv("E2E_TCP_TARGET")
	if targetURL == "" {
		targetURL = defaultTestTarget
	}

	bindAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("cannot get loopback addr: %v", err)
	}
	ensureTransportFlags()
	if err := flag.CommandLine.Set("bind", bindAddr); err != nil {
		t.Fatalf("set bind: %v", err)
	}
	if err := flag.CommandLine.Set("dest", bindAddr); err != nil {
		t.Fatalf("set dest: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverConnCh := make(chan transport.FrameConn, 1)
	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := transport.Open(ctx, transport.RoleServer, "tcp")
		if err != nil {
			serverErrCh <- err
			return
		}
		serverConnCh <- conn
	}()

	time.Sleep(100 * time.Millisecond)

	clientConnCh := make(chan transport.FrameConn, 1)
	clientErrCh := make(chan error, 1)
	go func() {
		conn, err := transport.Open(ctx, transport.RoleClient, "tcp")
		if err != nil {
			clientErrCh <- err
			return
		}
		clientConnCh <- conn
	}()

	var serverConn transport.FrameConn
	var clientConn transport.FrameConn
	for serverConn == nil || clientConn == nil {
		select {
		case conn := <-serverConnCh:
			serverConn = conn
		case conn := <-clientConnCh:
			clientConn = conn
		case err := <-serverErrCh:
			t.Fatalf("open server tcp: %v", err)
		case err := <-clientErrCh:
			t.Fatalf("open client tcp: %v", err)
		case <-ctx.Done():
			t.Fatalf("context canceled waiting for tcp endpoints: %v", ctx.Err())
		}
	}
	defer serverConn.Close()
	defer clientConn.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(ctx, server.Config{Transport: serverConn})
	}()

	socksAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("cannot allocate socks addr: %v", err)
	}
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run(ctx, client.Config{SocksListen: socksAddr, Transport: clientConn})
	}()

	// allow listener to come up
	time.Sleep(200 * time.Millisecond)

	httpClient := newHTTPClientThroughSOCKS(t, socksAddr)
	resp, err := httpClient.Get(targetURL)
	if err != nil {
		t.Fatalf("http request via tcp transport failed: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, body)
	}
	if len(body) == 0 {
		t.Fatalf("empty body from %s", targetURL)
	}

	cancel()
	waitErr(t, clientErr, "client")
	waitErr(t, serverErr, "server")
}

func TestLiveTCPTransportUDPProxy(t *testing.T) {
	if os.Getenv("E2E_TCP_UDP_ENABLE") == "" {
		t.Skip("set E2E_TCP_UDP_ENABLE=1 to enable live TCP UDP test")
	}

	bindAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("cannot get loopback addr: %v", err)
	}
	ensureTransportFlags()
	if err := flag.CommandLine.Set("bind", bindAddr); err != nil {
		t.Fatalf("set bind: %v", err)
	}
	if err := flag.CommandLine.Set("dest", bindAddr); err != nil {
		t.Fatalf("set dest: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverConnCh := make(chan transport.FrameConn, 1)
	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := transport.Open(ctx, transport.RoleServer, "tcp")
		if err != nil {
			serverErrCh <- err
			return
		}
		serverConnCh <- conn
	}()

	time.Sleep(100 * time.Millisecond)

	clientConnCh := make(chan transport.FrameConn, 1)
	clientErrCh := make(chan error, 1)
	go func() {
		conn, err := transport.Open(ctx, transport.RoleClient, "tcp")
		if err != nil {
			clientErrCh <- err
			return
		}
		clientConnCh <- conn
	}()

	var serverConn transport.FrameConn
	var clientConn transport.FrameConn
	for serverConn == nil || clientConn == nil {
		select {
		case conn := <-serverConnCh:
			serverConn = conn
		case conn := <-clientConnCh:
			clientConn = conn
		case err := <-serverErrCh:
			t.Fatalf("open server tcp: %v", err)
		case err := <-clientErrCh:
			t.Fatalf("open client tcp: %v", err)
		case <-ctx.Done():
			t.Fatalf("context canceled waiting for tcp endpoints: %v", ctx.Err())
		}
	}
	defer serverConn.Close()
	defer clientConn.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(ctx, server.Config{Transport: serverConn})
	}()

	socksAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("cannot allocate socks addr: %v", err)
	}
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run(ctx, client.Config{
			SocksListen: socksAddr,
			Transport:   clientConn,
		})
	}()

	time.Sleep(200 * time.Millisecond)
	exerciseUDPProxy(t, socksAddr)

	cancel()
	waitErr(t, clientErr, "client")
	waitErr(t, serverErr, "server")
}

func TestLiveUDSTransportProxy(t *testing.T) {
	if os.Getenv("E2E_UDS_ENABLE") == "" {
		t.Skip("set E2E_UDS_ENABLE=1 to enable live UDS transport test")
	}

	if runtime.GOOS == "windows" {
		t.Skip("unix domain sockets unsupported on this platform")
	}

	targetURL := os.Getenv("E2E_UDS_TARGET")
	if targetURL == "" {
		targetURL = defaultTestTarget
	}

	path := filepath.Join(os.TempDir(), fmt.Sprintf("transporter-uds-%d.sock", time.Now().UnixNano()))
	ensureTransportFlags()
	flag.CommandLine.Set("path", path)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverConnCh := make(chan transport.FrameConn, 1)
	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := transport.Open(ctx, transport.RoleServer, "uds")
		if err != nil {
			serverErrCh <- err
			return
		}
		serverConnCh <- conn
	}()

	time.Sleep(100 * time.Millisecond)

	clientConnCh := make(chan transport.FrameConn, 1)
	clientErrCh := make(chan error, 1)
	go func() {
		conn, err := transport.Open(ctx, transport.RoleClient, "uds")
		if err != nil {
			clientErrCh <- err
			return
		}
		clientConnCh <- conn
	}()

	var serverConn transport.FrameConn
	select {
	case conn := <-serverConnCh:
		serverConn = conn
	case err := <-serverErrCh:
		t.Fatalf("open server uds: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for uds server")
	}
	defer serverConn.Close()
	defer os.Remove(path)

	var clientConn transport.FrameConn
	select {
	case conn := <-clientConnCh:
		clientConn = conn
	case err := <-clientErrCh:
		t.Fatalf("open client uds: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for uds client")
	}
	defer clientConn.Close()

	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run(ctx, server.Config{Transport: serverConn}) }()

	socksAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("cannot allocate socks addr: %v", err)
	}
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run(ctx, client.Config{SocksListen: socksAddr, Transport: clientConn})
	}()

	time.Sleep(200 * time.Millisecond)
	resp, err := newHTTPClientThroughSOCKS(t, socksAddr).Get(targetURL)
	if err != nil {
		t.Fatalf("http request via uds transport failed: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, body)
	}
	if len(body) == 0 {
		t.Fatalf("empty body from %s", targetURL)
	}

	cancel()
	waitErr(t, clientErr, "client")
	waitErr(t, serverErr, "server")
}

func TestLiveUDSTransportUDPProxy(t *testing.T) {
	if os.Getenv("E2E_UDS_UDP_ENABLE") == "" {
		t.Skip("set E2E_UDS_UDP_ENABLE=1 to enable live UDS UDP test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("unix domain sockets unsupported on this platform")
	}

	path := filepath.Join(os.TempDir(), fmt.Sprintf("transporter-uds-%d.sock", time.Now().UnixNano()))
	ensureTransportFlags()
	flag.CommandLine.Set("path", path)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverConnCh := make(chan transport.FrameConn, 1)
	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := transport.Open(ctx, transport.RoleServer, "uds")
		if err != nil {
			serverErrCh <- err
			return
		}
		serverConnCh <- conn
	}()

	time.Sleep(100 * time.Millisecond)

	clientConnCh := make(chan transport.FrameConn, 1)
	clientErrCh := make(chan error, 1)
	go func() {
		conn, err := transport.Open(ctx, transport.RoleClient, "uds")
		if err != nil {
			clientErrCh <- err
			return
		}
		clientConnCh <- conn
	}()

	var serverConn transport.FrameConn
	select {
	case conn := <-serverConnCh:
		serverConn = conn
	case err := <-serverErrCh:
		t.Fatalf("open server uds: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for uds server")
	}
	defer serverConn.Close()
	defer os.Remove(path)

	var clientConn transport.FrameConn
	select {
	case conn := <-clientConnCh:
		clientConn = conn
	case err := <-clientErrCh:
		t.Fatalf("open client uds: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for uds client")
	}
	defer clientConn.Close()

	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run(ctx, server.Config{Transport: serverConn}) }()

	socksAddr, err := freeLoopback()
	if err != nil {
		t.Skipf("cannot allocate socks addr: %v", err)
	}
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run(ctx, client.Config{
			SocksListen: socksAddr,
			Transport:   clientConn,
		})
	}()

	time.Sleep(200 * time.Millisecond)
	exerciseUDPProxy(t, socksAddr)

	cancel()
	waitErr(t, clientErr, "client")
	waitErr(t, serverErr, "server")
}

func exerciseUDPProxy(t *testing.T, socksAddr string) {
	t.Helper()
	udpTarget, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("bind udp target: %v", err)
	}
	defer udpTarget.Close()
	go runUDPEcho(udpTarget)

	clientUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp client: %v", err)
	}
	defer clientUDP.Close()

	localConn, udpBind := startUDPAssociation(t, socksAddr, clientUDP.LocalAddr().(*net.UDPAddr))
	t.Cleanup(func() { localConn.Close() })

	payload := []byte("udp-live-test")
	packet, err := buildSocksUDPPacket(udpTarget.LocalAddr().(*net.UDPAddr), payload)
	if err != nil {
		t.Fatalf("build udp packet: %v", err)
	}
	if _, err := clientUDP.WriteToUDP(packet, udpBind); err != nil {
		t.Fatalf("send udp packet: %v", err)
	}

	if err := clientUDP.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 64*1024)
	n, _, err := clientUDP.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read udp reply: %v", err)
	}
	host, port, data, err := parseSocksUDPPacket(buf[:n])
	if err != nil {
		t.Fatalf("parse udp reply: %v", err)
	}
	targetAddr := udpTarget.LocalAddr().(*net.UDPAddr)
	if host != targetAddr.IP.String() || port != targetAddr.Port {
		t.Fatalf("unexpected udp origin %s:%d", host, port)
	}
	expected := bytes.ToUpper(payload)
	if !bytes.Equal(data, expected) {
		t.Fatalf("unexpected udp payload: got %q want %q", data, expected)
	}
}

func socks5Connect(conn net.Conn, host string, port int) error {
	if err := socks5Negotiate(conn); err != nil {
		return err
	}
	if err := writeSocksRequest(conn, 0x01, host, port); err != nil {
		return err
	}
	rep, _, _, err := readSocksReply(conn)
	if err != nil {
		return err
	}
	if rep != 0x00 {
		return fmt.Errorf("socks connect failed: %d", rep)
	}
	return nil
}

func socks5Negotiate(conn net.Conn) error {
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[1] != 0x00 {
		return errors.New("socks server rejected method")
	}
	return nil
}

func startUDPAssociation(
	t *testing.T,
	socksAddr string,
	clientAddr *net.UDPAddr,
) (net.Conn, *net.UDPAddr) {
	t.Helper()
	var (
		conn     net.Conn
		udpAddr  *net.UDPAddr
		lastErr  error
		deadline = time.Now().Add(5 * time.Second)
	)
	for time.Now().Before(deadline) {
		conn, lastErr = dialSOCKSConn(socksAddr, 2*time.Second)
		if lastErr != nil {
			continue
		}
		if lastErr = socks5Negotiate(conn); lastErr != nil {
			conn.Close()
			continue
		}
		host := "0.0.0.0"
		port := 0
		if clientAddr != nil {
			host = clientAddr.IP.String()
			port = clientAddr.Port
		}
		if lastErr = writeSocksRequest(conn, 0x03, host, port); lastErr != nil {
			conn.Close()
			continue
		}
		var bindHost string
		var bindPort int
		var rep byte
		rep, bindHost, bindPort, lastErr = readSocksReply(conn)
		if lastErr != nil || rep != 0x00 {
			conn.Close()
			if lastErr == nil {
				lastErr = fmt.Errorf("udp associate failed: %d", rep)
			}
			continue
		}
		ip := net.ParseIP(bindHost)
		if ip == nil {
			conn.Close()
			lastErr = fmt.Errorf("invalid bind host %q", bindHost)
			continue
		}
		udpAddr = &net.UDPAddr{IP: ip, Port: bindPort}
		return conn, udpAddr
	}
	t.Fatalf("failed to create UDP association: %v", lastErr)
	return nil, nil
}

func writeSocksRequest(conn net.Conn, cmd byte, host string, port int) error {
	buf := []byte{0x05, cmd, 0x00}
	ip := net.ParseIP(host)
	if ip4 := ip.To4(); ip4 != nil {
		buf = append(buf, 0x01)
		buf = append(buf, ip4...)
	} else if ip6 := ip.To16(); ip6 != nil {
		buf = append(buf, 0x04)
		buf = append(buf, ip6...)
	} else {
		if len(host) > 255 {
			return fmt.Errorf("host too long")
		}
		buf = append(buf, 0x03, byte(len(host)))
		buf = append(buf, []byte(host)...)
	}
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	buf = append(buf, portBytes...)
	_, err := conn.Write(buf)
	return err
}

func readSocksReply(conn net.Conn) (byte, string, int, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return 0, "", 0, err
	}
	atyp := header[3]
	var host string
	switch atyp {
	case 0x01:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return 0, "", 0, err
		}
		host = net.IP(addr).String()
	case 0x04:
		addr := make([]byte, 16)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return 0, "", 0, err
		}
		host = net.IP(addr).String()
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return 0, "", 0, err
		}
		addr := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, addr); err != nil {
			return 0, "", 0, err
		}
		host = string(addr)
	default:
		return 0, "", 0, fmt.Errorf("unknown atyp %d", atyp)
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return 0, "", 0, err
	}
	port := int(binary.BigEndian.Uint16(portBytes))
	return header[1], host, port, nil
}

func runUDPEcho(conn *net.UDPConn) {
	buf := make([]byte, 64*1024)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		resp := bytes.ToUpper(buf[:n])
		_, _ = conn.WriteToUDP(resp, addr)
	}
}

func buildSocksUDPPacket(target *net.UDPAddr, payload []byte) ([]byte, error) {
	buf := &bytes.Buffer{}
	buf.Write([]byte{0x00, 0x00, 0x00})
	ip := target.IP
	if ip4 := ip.To4(); ip4 != nil {
		buf.WriteByte(0x01)
		buf.Write(ip4)
	} else if ip6 := ip.To16(); ip6 != nil {
		buf.WriteByte(0x04)
		buf.Write(ip6)
	} else {
		return nil, fmt.Errorf("invalid target ip %v", target.IP)
	}
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(target.Port))
	buf.Write(portBytes)
	buf.Write(payload)
	return buf.Bytes(), nil
}

func parseSocksUDPPacket(packet []byte) (host string, port int, payload []byte, err error) {
	if len(packet) < 4 {
		return "", 0, nil, fmt.Errorf("packet too short")
	}
	if packet[2] != 0x00 {
		return "", 0, nil, fmt.Errorf("fragmented packet not supported")
	}
	atyp := packet[3]
	offset := 4
	switch atyp {
	case 0x01:
		if len(packet) < offset+4+2 {
			return "", 0, nil, fmt.Errorf("invalid ipv4 packet")
		}
		host = net.IP(packet[offset : offset+4]).String()
		offset += 4
	case 0x04:
		if len(packet) < offset+16+2 {
			return "", 0, nil, fmt.Errorf("invalid ipv6 packet")
		}
		host = net.IP(packet[offset : offset+16]).String()
		offset += 16
	case 0x03:
		if len(packet) < offset+1 {
			return "", 0, nil, fmt.Errorf("invalid domain packet")
		}
		l := int(packet[offset])
		offset++
		if len(packet) < offset+l+2 {
			return "", 0, nil, fmt.Errorf("invalid domain length")
		}
		host = string(packet[offset : offset+l])
		offset += l
	default:
		return "", 0, nil, fmt.Errorf("unknown atyp %d", atyp)
	}
	if len(packet) < offset+2 {
		return "", 0, nil, fmt.Errorf("missing port")
	}
	port = int(binary.BigEndian.Uint16(packet[offset : offset+2]))
	offset += 2
	payload = append([]byte(nil), packet[offset:]...)
	return host, port, payload, nil
}

type memConn struct {
	send      chan *proto.Frame
	recv      <-chan *proto.Frame
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
}

func newMemPair() (transport.FrameConn, transport.FrameConn) {
	c2s := make(chan *proto.Frame, 32)
	s2c := make(chan *proto.Frame, 32)
	return &memConn{send: c2s, recv: s2c, closed: make(chan struct{})},
		&memConn{send: s2c, recv: c2s, closed: make(chan struct{})}
}

func (m *memConn) Send(ctx context.Context, frame *proto.Frame) error {
	cloned := cloneFrame(frame)
	m.mu.Lock()
	sendCh := m.send
	m.mu.Unlock()
	if sendCh == nil {
		return io.EOF
	}
	select {
	case sendCh <- cloned:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-m.closed:
		return io.EOF
	}
}

func (m *memConn) Receive(ctx context.Context) (*proto.Frame, error) {
	select {
	case frame, ok := <-m.recv:
		if !ok {
			return nil, io.EOF
		}
		return frame, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.closed:
		return nil, io.EOF
	}
}

func (m *memConn) Close() error {
	m.closeOnce.Do(func() {
		close(m.closed)
		m.mu.Lock()
		if m.send != nil {
			close(m.send)
			m.send = nil
		}
		m.mu.Unlock()
	})
	return nil
}

func cloneFrame(f *proto.Frame) *proto.Frame {
	data := make([]byte, len(f.Payload))
	copy(data, f.Payload)
	return &proto.Frame{
		Version:       f.Version,
		Command:       f.Command,
		Proto:         f.Proto,
		Stream:        f.Stream,
		TargetAddress: f.TargetAddress,
		TargetPort:    f.TargetPort,
		Payload:       data,
		Error:         f.Error,
	}
}

func waitErr(t *testing.T, ch <-chan error, label string) {
	t.Helper()
	select {
	case err := <-ch:
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
				return
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				return
			}
			if strings.Contains(err.Error(), "use of closed network connection") {
				return
			}
			t.Fatalf("%s error: %v", label, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s shutdown", label)
	}
}

func dialSOCKSConn(addr string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func freeLoopback() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr, nil
}

var registerTransportFlags sync.Once

func ensureTransportFlags() {
	registerTransportFlags.Do(func() {
		transport.AddFlags(flag.CommandLine)
	})
}

func newHTTPClientThroughSOCKS(t *testing.T, socksAddr string) *http.Client {
	t.Helper()
	baseDialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, baseDialer)
	if err != nil {
		t.Fatalf("create socks dialer: %v", err)
	}
	contextDialer := socksContextDialer{dialer: dialer}
	tr := &http.Transport{
		DialContext:           contextDialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   30 * time.Second,
		MaxIdleConns:          2,
		IdleConnTimeout:       10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   60 * time.Second,
	}
}

type socksContextDialer struct {
	dialer proxy.Dialer
}

func (s socksContextDialer) DialContext(
	ctx context.Context,
	network,
	address string,
) (net.Conn, error) {
	if cd, ok := s.dialer.(interface {
		DialContext(context.Context, string, string) (net.Conn, error)
	}); ok {
		return cd.DialContext(ctx, network, address)
	}
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := s.dialer.Dial(network, address)
		ch <- result{conn: conn, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		return res.conn, res.err
	}
}
