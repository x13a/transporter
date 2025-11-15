package proto

import (
	"bytes"
	"hash/crc32"
	"testing"
)

func TestFrameChecksumRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	dec := NewDecoder(&buf)

	frame := &Frame{
		Version:       Version,
		Command:       CommandData,
		Proto:         ProtocolTCP,
		Flags:         FlagChecksum,
		Stream:        42,
		TargetAddress: "example.org",
		TargetPort:    443,
		Payload:       []byte("hello checksum"),
	}
	if err := enc.WriteFrame(frame); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := dec.ReadFrame()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Flags&FlagChecksum == 0 {
		t.Fatalf("expected checksum flag to propagate")
	}
	wantSum := crc32.ChecksumIEEE(frame.Payload)
	if got.Checksum != wantSum {
		t.Fatalf("checksum mismatch: got %08x want %08x", got.Checksum, wantSum)
	}
}

func TestFrameChecksumMismatch(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)

	frame := &Frame{
		Version:       Version,
		Command:       CommandData,
		Proto:         ProtocolTCP,
		Flags:         FlagChecksum,
		Stream:        7,
		TargetAddress: "host",
		TargetPort:    80,
		Payload:       []byte("payload"),
	}
	if err := enc.WriteFrame(frame); err != nil {
		t.Fatalf("encode: %v", err)
	}

	data := append([]byte(nil), buf.Bytes()...)
	if len(data) == 0 {
		t.Fatal("encoded frame empty")
	}
	data[len(data)-1] ^= 0xFF

	dec := NewDecoder(bytes.NewReader(data))
	if _, err := dec.ReadFrame(); err == nil {
		t.Fatalf("expected checksum mismatch error")
	}
}
