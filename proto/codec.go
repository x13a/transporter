package proto

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// Encoder serializes frames to an underlying io.Writer.
type Encoder struct {
	w  io.Writer
	mu sync.Mutex
}

// Decoder deserializes frames from an io.Reader.
type Decoder struct {
	r io.Reader
}

// NewEncoder wraps the writer with a frame encoder.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

// NewDecoder wraps the reader with a frame decoder.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: r}
}

// WriteFrame writes a single frame to the underlying writer, enforcing length limits.
func (e *Encoder) WriteFrame(frame *Frame) error {
	if frame.Version == 0 {
		frame.Version = Version
	}
	if err := frame.Validate(); err != nil {
		return err
	}

	var header bytes.Buffer
	header.Grow(18 + len(frame.TargetAddress) + len(frame.Error) + len(frame.Payload))

	header.WriteByte(frame.Version)
	header.WriteByte(byte(frame.Command))
	header.WriteByte(byte(frame.Proto))
	header.WriteByte(0) // Reserved flags for later use.

	tmp := make([]byte, 4)
	putUint32(tmp, frame.Stream)
	header.Write(tmp)

	putUint16(tmp[:2], uint16(len(frame.TargetAddress)))
	header.Write(tmp[:2])
	header.WriteString(frame.TargetAddress)

	putUint16(tmp[:2], frame.TargetPort)
	header.Write(tmp[:2])

	putUint16(tmp[:2], uint16(len(frame.Error)))
	header.Write(tmp[:2])
	header.WriteString(frame.Error)

	putUint32(tmp, uint32(len(frame.Payload)))
	header.Write(tmp)
	header.Write(frame.Payload)

	e.mu.Lock()
	defer e.mu.Unlock()

	_, err := e.w.Write(header.Bytes())
	return err
}

// ReadFrame consumes a frame from the underlying reader. It blocks until a full frame
// is available.
func (d *Decoder) ReadFrame() (*Frame, error) {
	readBytes := func(n int) ([]byte, error) {
		buf := make([]byte, n)
		if _, err := io.ReadFull(d.r, buf); err != nil {
			return nil, err
		}
		return buf, nil
	}

	h, err := readBytes(4)
	if err != nil {
		return nil, err
	}
	frame := &Frame{
		Version: h[0],
		Command: Command(h[1]),
		Proto:   Protocol(h[2]),
	}
	if frame.Version != Version {
		return nil, fmt.Errorf("unsupported frame version %d", frame.Version)
	}

	streamBytes, err := readBytes(4)
	if err != nil {
		return nil, err
	}
	frame.Stream = readUint32(streamBytes)
	if frame.Stream == 0 {
		return nil, fmt.Errorf("invalid stream id 0")
	}

	addrLenBytes, err := readBytes(2)
	if err != nil {
		return nil, err
	}
	addrLen := int(readUint16(addrLenBytes))
	if addrLen > 0 {
		addr, err := readBytes(addrLen)
		if err != nil {
			return nil, err
		}
		frame.TargetAddress = string(addr)
	}

	portBytes, err := readBytes(2)
	if err != nil {
		return nil, err
	}
	frame.TargetPort = readUint16(portBytes)

	errLenBytes, err := readBytes(2)
	if err != nil {
		return nil, err
	}
	errLen := int(readUint16(errLenBytes))
	if errLen > 0 {
		errBuf, err := readBytes(errLen)
		if err != nil {
			return nil, err
		}
		frame.Error = string(errBuf)
	}

	payloadLenBytes, err := readBytes(4)
	if err != nil {
		return nil, err
	}
	payloadLen := int(binary.BigEndian.Uint32(payloadLenBytes))
	if payloadLen > MaxPayloadLen {
		return nil, fmt.Errorf("frame payload too large: %d", payloadLen)
	}
	if payloadLen > 0 {
		payload, err := readBytes(payloadLen)
		if err != nil {
			return nil, err
		}
		frame.Payload = payload
	}

	return frame, nil
}
