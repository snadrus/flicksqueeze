package vfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"
)

// SFTP implements FS over an SSH connection.
type SFTP struct {
	client *sftp.Client
	sshc   *ssh.Client
}

// DialSSH parses an ssh:// URL, connects, and returns the FS and remote root path.
// Format: ssh://user@host[:port]/path
// Tries SSH agent first, then prompts for a password.
func DialSSH(rawURL string) (*SFTP, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("invalid ssh URL: %w", err)
	}
	if u.Scheme != "ssh" {
		return nil, "", fmt.Errorf("expected ssh:// scheme, got %q", u.Scheme)
	}

	user := u.User.Username()
	if user == "" {
		user = os.Getenv("USER")
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "22"
	}
	remotePath := u.Path
	if remotePath == "" {
		remotePath = "/"
	}

	var authMethods []ssh.AuthMethod

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	authMethods = append(authMethods, ssh.PasswordCallback(func() (string, error) {
		fmt.Fprintf(os.Stderr, "Password for %s@%s: ", user, host)
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(pw), err
	}))

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	addr := net.JoinHostPort(host, port)
	log.Printf("connecting to %s as %s...", addr, user)
	sshc, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, "", fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	sc, err := sftp.NewClient(sshc)
	if err != nil {
		sshc.Close()
		return nil, "", fmt.Errorf("sftp session: %w", err)
	}

	log.Printf("connected to %s:%s, root=%s", host, port, remotePath)
	return &SFTP{client: sc, sshc: sshc}, remotePath, nil
}

func (s *SFTP) Close() error {
	s.client.Close()
	return s.sshc.Close()
}

// ---- FS interface ----

func (s *SFTP) Walk(root string, fn fs.WalkDirFunc) error {
	walker := s.client.Walk(root)
	for walker.Step() {
		if walker.Err() != nil {
			if err := fn(walker.Path(), nil, walker.Err()); err != nil {
				return err
			}
			continue
		}
		info := walker.Stat()
		entry := fs.FileInfoToDirEntry(info)
		if err := fn(walker.Path(), entry, nil); err != nil {
			if err == fs.SkipDir {
				walker.SkipDir()
				continue
			}
			return err
		}
	}
	return nil
}

func (s *SFTP) Stat(path string) (fs.FileInfo, error) {
	return s.client.Stat(path)
}

func (s *SFTP) Open(path string) (io.ReadCloser, error) {
	return s.client.Open(path)
}

func (s *SFTP) Create(path string) (io.WriteCloser, error) {
	return s.client.Create(path)
}

func (s *SFTP) OpenFile(path string, flag int, perm os.FileMode) (File, error) {
	return s.client.OpenFile(path, flag)
}

func (s *SFTP) Remove(path string) error {
	return s.client.Remove(path)
}

func (s *SFTP) Rename(oldpath, newpath string) error {
	return s.client.Rename(oldpath, newpath)
}

func (s *SFTP) MkdirAll(path string, perm os.FileMode) error {
	return s.client.MkdirAll(path)
}

func (s *SFTP) CopyToLocal(remotePath, localPath string) error {
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	src, err := s.client.Open(remotePath)
	if err != nil {
		return fmt.Errorf("sftp open %s: %w", remotePath, err)
	}
	defer src.Close()
	dst, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer dst.Close()
	size, err := io.Copy(dst, src)
	if err != nil {
		return fmt.Errorf("download %s: %w", remotePath, err)
	}
	log.Printf("downloaded %s (%s)", remotePath, humanBytes(size))
	return nil
}

func (s *SFTP) CopyFromLocal(localPath, remotePath string) error {
	src, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := s.client.Create(remotePath)
	if err != nil {
		return fmt.Errorf("sftp create %s: %w", remotePath, err)
	}
	defer dst.Close()
	size, err := io.Copy(dst, src)
	if err != nil {
		return fmt.Errorf("upload %s: %w", remotePath, err)
	}
	log.Printf("uploaded %s (%s)", remotePath, humanBytes(size))
	return nil
}

func (s *SFTP) Exec(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	sess, err := s.sshc.NewSession()
	if err != nil {
		return nil, nil, fmt.Errorf("ssh session: %w", err)
	}
	defer sess.Close()

	cmdLine := name
	for _, a := range args {
		cmdLine += " " + shellQuote(a)
	}

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmdLine) }()

	select {
	case err := <-done:
		return stdout.Bytes(), stderr.Bytes(), err
	case <-ctx.Done():
		sess.Signal(ssh.SIGTERM)
		return nil, nil, ctx.Err()
	}
}

func (s *SFTP) IsRemote() bool { return true }

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n\"'\\$`!#&|;(){}[]<>?*~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
