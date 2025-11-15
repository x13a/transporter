package transport

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"transporter/proto"
)

const (
	dirClientToServer = "client_to_server"
	dirServerToClient = "server_to_client"
)

type dirConn struct {
	sendDir    string
	recvDir    string
	baseDir    string
	removeBase bool
}

func newFileConn(baseDir string, role Role) (FrameConn, error) {
	removeBase := false
	if baseDir == "" {
		tempDir, err := os.MkdirTemp("", "transporter-file-*")
		if err != nil {
			return nil, fmt.Errorf("create temp dir: %w", err)
		}
		baseDir = tempDir
		removeBase = true
	}
	sendDir, recvDir := deriveDirs(baseDir, role)
	for _, dir := range []string{sendDir, recvDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	return &dirConn{
		sendDir:    sendDir,
		recvDir:    recvDir,
		baseDir:    baseDir,
		removeBase: removeBase,
	}, nil
}

func deriveDirs(base string, role Role) (sendDir, recvDir string) {
	if role == RoleClient {
		return filepath.Join(base, dirClientToServer), filepath.Join(base, dirServerToClient)
	}
	return filepath.Join(base, dirServerToClient), filepath.Join(base, dirClientToServer)
}

func (f *dirConn) Send(ctx context.Context, frame *proto.Frame) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	data, err := proto.EncodeFrame(frame)
	if err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(f.sendDir, ".frame-*")
	if err != nil {
		return err
	}
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpFile.Name())
		return err
	}
	finalName := filepath.Join(f.sendDir, nextFrameFilename())
	if err := os.Rename(tmpFile.Name(), finalName); err != nil {
		_ = os.Remove(tmpFile.Name())
		return err
	}
	return nil
}

var frameSeq uint64

func nextFrameFilename() string {
	seq := atomic.AddUint64(&frameSeq, 1)
	return fmt.Sprintf("%d-%08d.frame", time.Now().UnixNano(), seq)
}

func (f *dirConn) Receive(ctx context.Context) (*proto.Frame, error) {
	for {
		frame, err := f.tryReadFrame()
		if err != nil {
			return nil, err
		}
		if frame != nil {
			return frame, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (f *dirConn) tryReadFrame() (*proto.Frame, error) {
	entries, err := os.ReadDir(f.recvDir)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", f.recvDir, err)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(f.recvDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read frame %s: %w", path, err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("remove frame %s: %w", path, err)
		}
		frame, err := proto.DecodeFrame(data)
		if err != nil {
			return nil, fmt.Errorf("decode frame %s: %w", path, err)
		}
		return frame, nil
	}
	return nil, nil
}

func (f *dirConn) Close() error {
	if f.removeBase && f.baseDir != "" {
		cleanupDir := func(dir string) {
			entries, err := os.ReadDir(dir)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return
			}
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				path := filepath.Join(dir, entry.Name())
				_ = os.Remove(path)
			}
			_ = os.Remove(dir)
		}
		cleanupDir(filepath.Join(f.baseDir, dirClientToServer))
		cleanupDir(filepath.Join(f.baseDir, dirServerToClient))
		return os.Remove(f.baseDir)
	}
	return nil
}
