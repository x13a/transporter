package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"

	"transporter/proto"
	"transporter/transport"
)

// Config provides the frame transport required by the server side.
type Config struct {
	Transport   transport.FrameConn
	Verbose     bool
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

// Run starts the forwarding loop. It blocks until the context is canceled or the transport fails.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Transport == nil {
		return errors.New("transport is required")
	}
	localCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	srv := &Server{
		conn:       cfg.Transport,
		streams:    make(map[uint32]*backendStream),
		udpStreams: make(map[uint32]*udpSession),
		verbose:    cfg.Verbose,
		dial:       cfg.DialContext,
	}
	if srv.dial == nil {
		srv.dial = defaultDialContext
	}
	err := srv.loop(localCtx)
	_ = cfg.Transport.Close()
	srv.wg.Wait()
	if err != nil && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

type Server struct {
	conn       transport.FrameConn
	streams    map[uint32]*backendStream
	mu         sync.RWMutex
	udpStreams map[uint32]*udpSession
	udpMu      sync.RWMutex
	verbose    bool
	dial       func(ctx context.Context, network, address string) (net.Conn, error)
	wg         sync.WaitGroup
}

func defaultDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, address)
}

func (s *Server) loop(ctx context.Context) error {
	for {
		frame, err := s.conn.Receive(ctx)
		if err != nil {
			s.closeAll()
			if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch frame.Command {
		case proto.CommandOpen:
			s.handleOpen(ctx, frame)
		case proto.CommandData:
			if frame.Proto == proto.ProtocolUDP {
				s.handleUDPData(ctx, frame)
			} else {
				s.handleData(frame)
			}
		case proto.CommandClose:
			if frame.Proto == proto.ProtocolUDP {
				s.handleUDPClose(frame.Stream)
			} else {
				s.handleClose(frame.Stream)
			}
		case proto.CommandError:
			s.debugf("client reported error on stream %d: %s", frame.Stream, frame.Error)
		}
	}
}

func (s *Server) handleOpen(ctx context.Context, frame *proto.Frame) {
	if frame.Proto == proto.ProtocolUDP {
		s.handleUDPOpen(ctx, frame)
		return
	}
	addr := net.JoinHostPort(frame.TargetAddress, strconv.Itoa(int(frame.TargetPort)))
	conn, err := s.dial(ctx, "tcp", addr)
	if err != nil {
		s.debugf("dial %s failed: %v", addr, err)
		_ = s.conn.Send(ctx, &proto.Frame{
			Version: proto.Version,
			Command: proto.CommandError,
			Proto:   frame.Proto,
			Stream:  frame.Stream,
			Error:   fmt.Sprintf("dial %s: %v", addr, err),
		})
		return
	}

	stream := &backendStream{
		id:   frame.Stream,
		conn: conn,
		srv:  s,
	}
	s.setStream(frame.Stream, stream)
	s.debugf("opened backend stream %d to %s", frame.Stream, addr)
	s.wg.Add(1)
	go stream.pump(ctx)
}

func (s *Server) handleData(frame *proto.Frame) {
	stream := s.getStream(frame.Stream)
	if stream == nil {
		return
	}
	if len(frame.Payload) == 0 {
		return
	}
	_, _ = stream.conn.Write(frame.Payload)
}

func (s *Server) handleClose(id uint32) {
	stream := s.popStream(id)
	if stream == nil {
		return
	}
	_ = stream.conn.Close()
	s.debugf("backend stream %d closed by client", id)
}

func (s *Server) handleUDPOpen(ctx context.Context, frame *proto.Frame) {
	conn, err := net.ListenPacket("udp", "")
	if err != nil {
		s.debugf("udp listen failed: %v", err)
		_ = s.conn.Send(ctx, &proto.Frame{
			Version: proto.Version,
			Command: proto.CommandError,
			Proto:   frame.Proto,
			Stream:  frame.Stream,
			Error:   fmt.Sprintf("udp listen: %v", err),
		})
		return
	}
	session := &udpSession{
		id:   frame.Stream,
		conn: conn,
		srv:  s,
	}
	s.setUDPSession(frame.Stream, session)
	s.debugf("opened udp stream %d", frame.Stream)
	s.wg.Add(1)
	go session.pump(ctx)
}

func (s *Server) handleUDPData(ctx context.Context, frame *proto.Frame) {
	session := s.getUDPSession(frame.Stream)
	if session == nil {
		return
	}
	addr := net.JoinHostPort(frame.TargetAddress, strconv.Itoa(int(frame.TargetPort)))
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		s.debugf("resolve udp %s failed: %v", addr, err)
		return
	}
	payload := frame.Payload
	if payload == nil {
		payload = []byte{}
	}
	if _, err := session.conn.WriteTo(payload, udpAddr); err != nil {
		s.debugf("udp stream %d write error: %v", frame.Stream, err)
		session.conn.Close()
		s.removeUDPSession(frame.Stream)
	}
}

func (s *Server) handleUDPClose(id uint32) {
	session := s.removeUDPSession(id)
	if session == nil {
		return
	}
	_ = session.conn.Close()
	s.debugf("udp stream %d closed by client", id)
}

func (s *Server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, stream := range s.streams {
		stream.conn.Close()
		delete(s.streams, id)
		s.debugf("backend stream %d closed due to transport shutdown", id)
	}
	s.udpMu.Lock()
	for id, session := range s.udpStreams {
		session.conn.Close()
		delete(s.udpStreams, id)
		s.debugf("udp stream %d closed due to transport shutdown", id)
	}
	s.udpMu.Unlock()
}

func (s *Server) getStream(id uint32) *backendStream {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.streams[id]
}

func (s *Server) setStream(id uint32, stream *backendStream) {
	s.mu.Lock()
	s.streams[id] = stream
	s.mu.Unlock()
}

func (s *Server) popStream(id uint32) *backendStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	stream := s.streams[id]
	if stream != nil {
		delete(s.streams, id)
	}
	return stream
}

func (s *Server) setUDPSession(id uint32, session *udpSession) {
	s.udpMu.Lock()
	s.udpStreams[id] = session
	s.udpMu.Unlock()
}

func (s *Server) getUDPSession(id uint32) *udpSession {
	s.udpMu.RLock()
	defer s.udpMu.RUnlock()
	return s.udpStreams[id]
}

func (s *Server) removeUDPSession(id uint32) *udpSession {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	session := s.udpStreams[id]
	if session != nil {
		delete(s.udpStreams, id)
	}
	return session
}

func (s *Server) debugf(format string, args ...interface{}) {
	if s.verbose {
		log.Printf("[server] "+format, args...)
	}
}

type backendStream struct {
	id   uint32
	conn net.Conn
	srv  *Server
}

func (b *backendStream) pump(ctx context.Context) {
	defer func() {
		b.srv.wg.Done()
		b.srv.popStream(b.id)
	}()

	buf := make([]byte, 32*1024)
	for {
		n, err := b.conn.Read(buf)
		if n > 0 {
			payload := make([]byte, n)
			copy(payload, buf[:n])
			frame := &proto.Frame{
				Version: proto.Version,
				Command: proto.CommandData,
				Proto:   proto.ProtocolTCP,
				Stream:  b.id,
				Payload: payload,
			}
			if sendErr := b.srv.conn.Send(ctx, frame); sendErr != nil {
				err = sendErr
				break
			}
			b.srv.debugf("sent %d bytes on stream %d", n, b.id)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				_ = b.srv.conn.Send(ctx, &proto.Frame{
					Version: proto.Version,
					Command: proto.CommandError,
					Proto:   proto.ProtocolTCP,
					Stream:  b.id,
					Error:   err.Error(),
				})
			}
			b.srv.debugf("backend stream %d terminated: %v", b.id, err)
			break
		}
	}
	_ = b.srv.conn.Send(ctx, &proto.Frame{
		Version: proto.Version,
		Command: proto.CommandClose,
		Proto:   proto.ProtocolTCP,
		Stream:  b.id,
	})
	_ = b.conn.Close()
	b.srv.debugf("backend stream %d closed", b.id)
}

type udpSession struct {
	id   uint32
	conn net.PacketConn
	srv  *Server
}

func (u *udpSession) pump(ctx context.Context) {
	defer func() {
		u.conn.Close()
		u.srv.removeUDPSession(u.id)
		u.srv.wg.Done()
	}()

	go func() {
		<-ctx.Done()
		u.conn.Close()
	}()

	buf := make([]byte, 64*1024)
	for {
		n, addr, err := u.conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
				return
			}
			u.srv.debugf("udp stream %d read error: %v", u.id, err)
			return
		}
		host, portStr, err := net.SplitHostPort(addr.String())
		if err != nil {
			continue
		}
		portNum, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		payload := make([]byte, n)
		copy(payload, buf[:n])
		frame := &proto.Frame{
			Version:       proto.Version,
			Command:       proto.CommandData,
			Proto:         proto.ProtocolUDP,
			Stream:        u.id,
			TargetAddress: host,
			TargetPort:    uint16(portNum),
			Payload:       payload,
		}
		if err := u.srv.conn.Send(ctx, frame); err != nil {
			u.srv.debugf("udp stream %d relay failed: %v", u.id, err)
			return
		}
		u.srv.debugf("relayed %d udp bytes on stream %d", n, u.id)
	}
}
