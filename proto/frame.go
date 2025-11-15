package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Version identifies the framing protocol version.
const Version uint8 = 1

// MaxPayloadLen limits the maximum payload per frame to keep memory usage predictable.
const MaxPayloadLen = 1 << 20 // 1MiB per frame is enough for proxy data chunks.

// Command identifies what kind of frame is being exchanged.
type Command uint8

const (
	CommandOpen  Command = 1
	CommandData  Command = 2
	CommandClose Command = 3
	CommandError Command = 4
)

// Protocol identifies which transport protocol is carried inside the tunnel.
type Protocol uint8

const (
	ProtocolTCP Protocol = 1
	ProtocolUDP Protocol = 2
)

// Frame is the basic unit exchanged between the client-side SOCKS proxy and
// the server-side forwarder. The encoding format is the following:
//
//	byte 0   : Version
//	byte 1   : Command
//	byte 2   : Protocol
//	byte 3   : Flags (reserved for future use)
//	byte 4-7 : Stream ID (uint32, big endian)
//	byte 8-9 : Address length N (uint16)
//	byte 10..(10+N-1) : Address bytes
//	next 2 bytes      : Port (uint16)
//	next 2 bytes      : Error string length M
//	next M bytes      : Error string UTF-8
//	next 4 bytes      : Payload length P (uint32)
//	next P bytes      : Payload
//
// Fields that are not relevant to a specific command may be left empty. The
// decoder still consumes the reserved bytes, which keeps the framing fixed and
// easy to extend.
type Frame struct {
	Version uint8
	Command Command
	Proto   Protocol
	Stream  uint32

	TargetAddress string
	TargetPort    uint16

	Payload []byte
	Error   string
}

// Validate checks whether the frame complies with invariants that keep the proxy sane.
func (f *Frame) Validate() error {
	if f.Version != Version {
		return fmt.Errorf("unexpected frame version: %d", f.Version)
	}
	if f.Stream == 0 {
		return errors.New("stream id must be non-zero")
	}
	if len(f.Payload) > MaxPayloadLen {
		return fmt.Errorf("payload too large: %d bytes", len(f.Payload))
	}
	switch f.Command {
	case CommandOpen:
		if f.Proto == ProtocolTCP {
			if f.TargetAddress == "" || f.TargetPort == 0 {
				return errors.New("open frame requires target address and port")
			}
		}
	case CommandData:
		if f.Proto == ProtocolUDP {
			if f.TargetAddress == "" || f.TargetPort == 0 {
				return errors.New("udp data requires target")
			}
		}
	case CommandClose, CommandError:
	default:
		return fmt.Errorf("unknown command %d", f.Command)
	}
	return nil
}

func putUint16(b []byte, v uint16) {
	binary.BigEndian.PutUint16(b, v)
}

func putUint32(b []byte, v uint32) {
	binary.BigEndian.PutUint32(b, v)
}

func readUint8(buf []byte) uint8 {
	return buf[0]
}

func readUint16(buf []byte) uint16 {
	return binary.BigEndian.Uint16(buf)
}

func readUint32(buf []byte) uint32 {
	return binary.BigEndian.Uint32(buf)
}
