package vfs

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// Local implements FS using the OS filesystem and local exec.
type Local struct{}

func (Local) Walk(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, fn)
}

func (Local) Stat(path string) (fs.FileInfo, error) {
	return os.Stat(path)
}

func (Local) Open(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

func (Local) Create(path string) (io.WriteCloser, error) {
	return os.Create(path)
}

func (Local) OpenFile(path string, flag int, perm os.FileMode) (File, error) {
	return os.OpenFile(path, flag, perm)
}

func (Local) Remove(path string) error {
	return os.Remove(path)
}

func (Local) Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

func (Local) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (Local) CopyToLocal(remotePath, localPath string) error {
	if remotePath == localPath {
		return nil
	}
	src, err := os.Open(remotePath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return err
}

func (Local) CopyFromLocal(localPath, remotePath string) error {
	if localPath == remotePath {
		return nil
	}
	src, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(remotePath)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return err
}

func (Local) Exec(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return out, stderr.Bytes(), err
}

func (Local) IsRemote() bool { return false }
