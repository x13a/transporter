package proto

import (
	"bytes"
	"encoding/base64"
)

// EncodeFrame serializes a frame to its binary representation.
func EncodeFrame(frame *Frame) ([]byte, error) {
	buf := &bytes.Buffer{}
	if err := NewEncoder(buf).WriteFrame(frame); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeFrame deserializes a frame from raw bytes.
func DecodeFrame(data []byte) (*Frame, error) {
	return NewDecoder(bytes.NewReader(data)).ReadFrame()
}

// EncodeFrameString serializes the frame and returns a base64 string.
func EncodeFrameString(frame *Frame) (string, error) {
	data, err := EncodeFrame(frame)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// DecodeFrameString decodes a base64 string into a frame.
func DecodeFrameString(s string) (*Frame, error) {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return DecodeFrame(data)
}
