package vfs

import (
	"context"
	"io"
	"io/fs"
	"os"
)

// FS abstracts filesystem operations so the scanner and converter work
// transparently over local paths or SSH/SFTP remote hosts.
type FS interface {
	Walk(root string, fn fs.WalkDirFunc) error
	Stat(path string) (fs.FileInfo, error)
	Open(path string) (io.ReadCloser, error)
	Create(path string) (io.WriteCloser, error)
	OpenFile(path string, flag int, perm os.FileMode) (File, error)
	Remove(path string) error
	Rename(oldpath, newpath string) error
	MkdirAll(path string, perm os.FileMode) error

	// CopyToLocal downloads a remote file to a local path.
	// For the local backend this is a plain file copy.
	CopyToLocal(remotePath, localPath string) error

	// CopyFromLocal uploads a local file to a remote path.
	// For the local backend this is a plain file copy.
	CopyFromLocal(localPath, remotePath string) error

	// Exec runs a command (e.g. ffprobe) where the files live.
	// For local, this is exec.CommandContext; for SFTP, ssh.Session.
	Exec(ctx context.Context, name string, args ...string) (stdout []byte, stderr []byte, err error)

	// IsRemote returns true when the FS operates over a network.
	IsRemote() bool
}

// File is a minimal interface for files returned by OpenFile,
// supporting read, write, and close.
type File interface {
	io.Reader
	io.Writer
	io.Closer
}
