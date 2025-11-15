package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/txthinking/socks5"

	"transporter/proto"
	"transporter/transport"
)

// Config describes how the client component (SOCKS5 endpoint) should behave.
type Config struct {
	SocksListen string
	Transport   transport.FrameConn
	Verbose     bool
}

// Run starts the SOCKS5 listener and multiplexes streams across the transport.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Transport == nil {
		return errors.New("transport is required")
	}
	if cfg.SocksListen == "" {
		cfg.SocksListen = "127.0.0.1:1080"
	}

	cl := &Client{
		conn:      cfg.Transport,
		streams:   make(map[uint32]*clientStream),
		udpByID:   make(map[uint32]*udpAssociation),
		udpByAddr: make(map[string]*udpAssociation),
		verbose:   cfg.Verbose,
	}

	localCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cl.recvLoop(localCtx)
	}()

	srv, err := newSocksServer(cfg.SocksListen)
	if err != nil {
		cancel()
		wg.Wait()
		return err
	}
	handler := &socksProxyHandler{
		ctx:    localCtx,
		client: cl,
		server: srv,
	}
	cl.udpHandler = handler

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.ListenAndServe(handler)
	}()

	var runErr error
	select {
	case <-localCtx.Done():
		runErr = localCtx.Err()
	case err := <-serverErr:
		runErr = err
	}

	_ = srv.Shutdown()
	cancel()
	_ = cfg.Transport.Close()
	wg.Wait()

	if runErr == nil {
		return nil
	}
	if errors.Is(runErr, context.Canceled) ||
		errors.Is(runErr, net.ErrClosed) ||
		isClosedNetworkErr(runErr) {
		return nil
	}
	return runErr
}

type Client struct {
	conn    transport.FrameConn
	streams map[uint32]*clientStream
	mu      sync.RWMutex

	udpMu     sync.RWMutex
	udpByID   map[uint32]*udpAssociation
	udpByAddr map[string]*udpAssociation

	nextID     uint32
	verbose    bool
	udpHandler *socksProxyHandler
}

type udpAssociation struct {
	id         uint32
	clientAddr *net.UDPAddr
}

func (c *Client) recvLoop(ctx context.Context) {
	for {
		frame, err := c.conn.Receive(ctx)
		if err != nil {
			c.closeAll(err)
			return
		}
		if frame.Proto == proto.ProtocolUDP {
			c.handleUDPFrame(frame)
			continue
		}
		stream := c.getStream(frame.Stream)
		if stream == nil {
			continue
		}
		switch frame.Command {
		case proto.CommandData:
			stream.deliver(frame.Payload)
		case proto.CommandClose:
			stream.remoteClose(nil)
			c.removeStream(stream.id)
			c.debugf("stream %d closed by server", stream.id)
		case proto.CommandError:
			stream.remoteClose(errors.New(frame.Error))
			c.removeStream(stream.id)
			c.debugf("stream %d error from server: %s", stream.id, frame.Error)
		default:
		}
	}
}

func (c *Client) handleUDPFrame(frame *proto.Frame) {
	switch frame.Command {
	case proto.CommandData:
		if c.udpHandler == nil {
			return
		}
		if err := c.udpHandler.deliverUDP(frame); err != nil {
			c.debugf("udp stream %d delivery error: %v", frame.Stream, err)
		}
	case proto.CommandClose:
		if assoc := c.removeUDPAssociation(frame.Stream); assoc != nil {
			c.debugf("udp stream %d closed by server", frame.Stream)
		}
	case proto.CommandError:
		c.removeUDPAssociation(frame.Stream)
		if frame.Error != "" {
			c.debugf("udp stream %d error: %s", frame.Stream, frame.Error)
		}
	}
}

func (c *Client) sendTCPData(ctx context.Context, id uint32, payload []byte) error {
	for len(payload) > 0 {
		chunk := payload
		if len(chunk) > proto.MaxPayloadLen {
			chunk = payload[:proto.MaxPayloadLen]
		}
		payload = payload[len(chunk):]

		frame := &proto.Frame{
			Version: proto.Version,
			Command: proto.CommandData,
			Proto:   proto.ProtocolTCP,
			Stream:  id,
			Payload: chunk,
		}
		if err := c.conn.Send(ctx, frame); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) sendUDPData(
	ctx context.Context,
	id uint32,
	host string,
	port uint16,
	payload []byte,
) error {
	if len(payload) > proto.MaxPayloadLen {
		return fmt.Errorf("udp payload too large: %d bytes", len(payload))
	}
	buf := make([]byte, len(payload))
	copy(buf, payload)
	frame := &proto.Frame{
		Version:       proto.Version,
		Command:       proto.CommandData,
		Proto:         proto.ProtocolUDP,
		Stream:        id,
		TargetAddress: host,
		TargetPort:    port,
		Payload:       buf,
	}
	return c.conn.Send(ctx, frame)
}

func (c *Client) sendClose(ctx context.Context, id uint32, protoID proto.Protocol) error {
	frame := &proto.Frame{
		Version: proto.Version,
		Command: proto.CommandClose,
		Proto:   protoID,
		Stream:  id,
	}
	return c.conn.Send(ctx, frame)
}

func (c *Client) closeAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, stream := range c.streams {
		delete(c.streams, id)
		stream.remoteClose(err)
		c.debugf("stream %d closed due to transport error: %v", id, err)
	}
	c.udpMu.Lock()
	for id, assoc := range c.udpByID {
		delete(c.udpByAddr, assoc.clientAddr.String())
		delete(c.udpByID, id)
		c.debugf("udp stream %d closed due to transport error", id)
	}
	c.udpMu.Unlock()
}

func (c *Client) nextStreamID() uint32 {
	return atomic.AddUint32(&c.nextID, 1)
}

func (c *Client) getStream(id uint32) *clientStream {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.streams[id]
}

func (c *Client) setStream(id uint32, stream *clientStream) {
	c.mu.Lock()
	c.streams[id] = stream
	c.mu.Unlock()
}

func (c *Client) removeStream(id uint32) {
	c.mu.Lock()
	delete(c.streams, id)
	c.mu.Unlock()
}

func (c *Client) newUDPAssociation(addr *net.UDPAddr) *udpAssociation {
	id := c.nextStreamID()
	copied := &net.UDPAddr{
		IP:   append(net.IP(nil), addr.IP...),
		Port: addr.Port,
		Zone: addr.Zone,
	}
	assoc := &udpAssociation{
		id:         id,
		clientAddr: copied,
	}
	c.udpMu.Lock()
	c.udpByID[id] = assoc
	c.udpByAddr[copied.String()] = assoc
	c.udpMu.Unlock()
	return assoc
}

func (c *Client) udpAssociationByAddr(key string) *udpAssociation {
	c.udpMu.RLock()
	defer c.udpMu.RUnlock()
	return c.udpByAddr[key]
}

func (c *Client) udpAssociationByID(id uint32) *udpAssociation {
	c.udpMu.RLock()
	defer c.udpMu.RUnlock()
	return c.udpByID[id]
}

func (c *Client) removeUDPAssociation(id uint32) *udpAssociation {
	c.udpMu.Lock()
	defer c.udpMu.Unlock()
	assoc := c.udpByID[id]
	if assoc != nil {
		delete(c.udpByID, id)
		delete(c.udpByAddr, assoc.clientAddr.String())
	}
	return assoc
}

func (c *Client) closeUDPAssociation(ctx context.Context, id uint32) {
	if assoc := c.removeUDPAssociation(id); assoc != nil {
		_ = c.sendClose(ctx, id, proto.ProtocolUDP)
	}
}

func (c *Client) debugf(format string, args ...interface{}) {
	if c.verbose {
		log.Printf("[client] "+format, args...)
	}
}

type clientStream struct {
	id         uint32
	client     *Client
	ctx        context.Context
	incoming   chan []byte
	closed     chan struct{}
	localAddr  net.Addr
	remoteAddr net.Addr

	readBuf []byte

	closeOnce      sync.Once
	incomingCloser sync.Once
	streamErrMu    sync.RWMutex
	streamErr      error
}

func newClientStream(
	id uint32,
	client *Client,
	ctx context.Context,
	host string,
	port int,
) *clientStream {
	return &clientStream{
		id:         id,
		client:     client,
		ctx:        ctx,
		incoming:   make(chan []byte, 16),
		closed:     make(chan struct{}),
		localAddr:  &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)},
		remoteAddr: &net.TCPAddr{IP: net.ParseIP(host), Port: port},
	}
}

func (s *clientStream) Read(p []byte) (int, error) {
	if len(s.readBuf) == 0 {
		select {
		case data, ok := <-s.incoming:
			if !ok {
				return 0, s.currentErr()
			}
			s.readBuf = data
		}
	}
	n := copy(p, s.readBuf)
	s.readBuf = s.readBuf[n:]
	return n, nil
}

func (s *clientStream) Write(p []byte) (int, error) {
	if err := s.client.sendTCPData(s.ctx, s.id, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *clientStream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.client.sendClose(s.ctx, s.id, proto.ProtocolTCP)
		s.client.removeStream(s.id)
		s.incomingCloser.Do(func() {
			close(s.incoming)
		})
		close(s.closed)
	})
	return err
}

func (s *clientStream) deliver(payload []byte) {
	if payload == nil {
		payload = []byte{}
	}
	buf := make([]byte, len(payload))
	copy(buf, payload)
	defer func() {
		recover()
	}()
	select {
	case s.incoming <- buf:
	case <-s.closed:
	}
}

func (s *clientStream) remoteClose(err error) {
	s.streamErrMu.Lock()
	if err == nil {
		s.streamErr = io.EOF
	} else {
		s.streamErr = err
	}
	s.streamErrMu.Unlock()
	s.incomingCloser.Do(func() {
		close(s.incoming)
	})
}

func (s *clientStream) currentErr() error {
	s.streamErrMu.RLock()
	defer s.streamErrMu.RUnlock()
	if s.streamErr != nil {
		return s.streamErr
	}
	return io.EOF
}

func (s *clientStream) LocalAddr() net.Addr  { return s.localAddr }
func (s *clientStream) RemoteAddr() net.Addr { return s.remoteAddr }

func (s *clientStream) SetDeadline(_ time.Time) error      { return nil }
func (s *clientStream) SetReadDeadline(_ time.Time) error  { return nil }
func (s *clientStream) SetWriteDeadline(_ time.Time) error { return nil }

type socksProxyHandler struct {
	ctx    context.Context
	client *Client
	server *socks5.Server
}

func (h *socksProxyHandler) TCPHandle(
	_ *socks5.Server,
	conn *net.TCPConn,
	req *socks5.Request,
) error {
	switch req.Cmd {
	case socks5.CmdConnect:
		return h.handleConnect(conn, req)
	case socks5.CmdUDP:
		return h.handleUDPAssociate(conn, req)
	default:
		return h.writeReply(conn, socks5.RepCommandNotSupported)
	}
}

func (h *socksProxyHandler) UDPHandle(
	_ *socks5.Server,
	addr *net.UDPAddr,
	d *socks5.Datagram,
) error {
	assoc := h.client.udpAssociationByAddr(addr.String())
	if assoc == nil {
		return fmt.Errorf("udp datagram from %s without association", addr)
	}
	target := d.Address()
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	portNum, err := strconv.Atoi(portStr)
	if err != nil {
		return err
	}
	return h.client.sendUDPData(h.ctx, assoc.id, host, uint16(portNum), d.Data)
}

func (h *socksProxyHandler) deliverUDP(frame *proto.Frame) error {
	assoc := h.client.udpAssociationByID(frame.Stream)
	if assoc == nil {
		return fmt.Errorf("unknown udp stream %d", frame.Stream)
	}
	if h.server == nil || h.server.UDPConn == nil {
		return errors.New("udp listener not initialized")
	}
	addr := net.JoinHostPort(frame.TargetAddress, strconv.Itoa(int(frame.TargetPort)))
	atyp, dstAddr, dstPort, err := socks5.ParseAddress(addr)
	if err != nil {
		return err
	}
	dgram := socks5.NewDatagram(atyp, dstAddr, dstPort, append([]byte(nil), frame.Payload...))
	_, err = h.server.UDPConn.WriteToUDP(dgram.Bytes(), assoc.clientAddr)
	return err
}

func (h *socksProxyHandler) handleConnect(conn *net.TCPConn, req *socks5.Request) error {
	defer conn.Close()
	dest := req.Address()
	host, portStr, err := net.SplitHostPort(dest)
	if err != nil {
		h.writeReply(conn, socks5.RepHostUnreachable)
		return err
	}
	portNum, err := strconv.Atoi(portStr)
	if err != nil {
		h.writeReply(conn, socks5.RepHostUnreachable)
		return err
	}

	streamID := h.client.nextStreamID()
	stream := newClientStream(streamID, h.client, h.ctx, host, portNum)
	h.client.setStream(streamID, stream)

	openFrame := &proto.Frame{
		Version:       proto.Version,
		Command:       proto.CommandOpen,
		Proto:         proto.ProtocolTCP,
		Stream:        streamID,
		TargetAddress: host,
		TargetPort:    uint16(portNum),
	}
	if err := h.client.conn.Send(h.ctx, openFrame); err != nil {
		h.client.removeStream(streamID)
		h.writeReply(conn, socks5.RepServerFailure)
		return err
	}
	if err := h.writeReply(conn, socks5.RepSuccess); err != nil {
		h.client.removeStream(streamID)
		return err
	}
	h.client.debugf("opened stream %d to %s", streamID, dest)

	go func() {
		<-h.ctx.Done()
		conn.Close()
	}()

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		_, _ = io.Copy(stream, conn)
		stream.Close()
	}()
	_, _ = io.Copy(conn, stream)
	<-clientDone
	return nil
}

func (h *socksProxyHandler) handleUDPAssociate(conn *net.TCPConn, req *socks5.Request) error {
	defer conn.Close()
	if h.server == nil {
		return errors.New("socks server not initialized")
	}
	clientAddr, err := req.UDP(conn, h.server.ServerAddr)
	if err != nil {
		return err
	}
	udpAddr, ok := clientAddr.(*net.UDPAddr)
	if !ok {
		return fmt.Errorf("unexpected udp addr type %T", clientAddr)
	}
	assoc := h.client.newUDPAssociation(udpAddr)
	openFrame := &proto.Frame{
		Version: proto.Version,
		Command: proto.CommandOpen,
		Proto:   proto.ProtocolUDP,
		Stream:  assoc.id,
	}
	if err := h.client.conn.Send(h.ctx, openFrame); err != nil {
		h.client.removeUDPAssociation(assoc.id)
		return err
	}
	h.client.debugf("opened udp association %d for %s", assoc.id, clientAddr)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(io.Discard, conn)
	}()
	select {
	case <-done:
	case <-h.ctx.Done():
		conn.Close()
		<-done
	}
	h.client.closeUDPAssociation(h.ctx, assoc.id)
	return nil
}

func (h *socksProxyHandler) writeReply(conn *net.TCPConn, rep byte) error {
	var (
		atyp byte
		addr []byte
		port []byte
		err  error
	)
	if conn.LocalAddr() != nil {
		atyp, addr, port, err = socks5.ParseAddress(conn.LocalAddr().String())
	}
	if err != nil {
		atyp = socks5.ATYPIPv4
		addr = []byte{0x00, 0x00, 0x00, 0x00}
		port = []byte{0x00, 0x00}
	}
	reply := socks5.NewReply(rep, atyp, addr, port)
	_, err = reply.WriteTo(conn)
	return err
}

func newSocksServer(addr string) (*socks5.Server, error) {
	host := socksBindIP(addr)
	srv, err := socks5.NewClassicServer(addr, host, "", "", 0, 0)
	if err != nil {
		return nil, err
	}
	srv.LimitUDP = false
	return srv, nil
}

func socksBindIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" || host == "::" || host == "0.0.0.0" {
		return "127.0.0.1"
	}
	return host
}

func isClosedNetworkErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}
