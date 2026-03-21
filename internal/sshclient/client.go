package sshclient

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/anthropics/edgessh/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// Connect establishes an SSH client connection over an existing net.Conn
// (typically a WebSocket). Returns the ssh.Client ready for use.
func Connect(conn net.Conn) (*ssh.Client, error) {
	keyData, err := os.ReadFile(config.PrivateKeyPath())
	if err != nil {
		return nil, fmt.Errorf("reading private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User: "cloudchamber",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	c, chans, reqs, err := ssh.NewClientConn(conn, "edgessh", sshConfig)
	if err != nil {
		return nil, fmt.Errorf("SSH handshake: %w", err)
	}

	return ssh.NewClient(c, chans, reqs), nil
}

// Shell opens an interactive bash login shell.
func Shell(client *ssh.Client) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	// Put local terminal into raw mode
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("setting raw terminal: %w", err)
		}
		defer term.Restore(fd, oldState)

		w, h, _ := term.GetSize(fd)
		if err := session.RequestPty("xterm-256color", h, w, ssh.TerminalModes{
			ssh.ECHO:          1,
			ssh.TTY_OP_ISPEED: 14400,
			ssh.TTY_OP_OSPEED: 14400,
		}); err != nil {
			return fmt.Errorf("requesting PTY: %w", err)
		}

		// Handle terminal resize
		sigWinch := make(chan os.Signal, 1)
		signal.Notify(sigWinch, syscall.SIGWINCH)
		go func() {
			for range sigWinch {
				w, h, _ := term.GetSize(fd)
				session.WindowChange(h, w)
			}
		}()
		defer signal.Stop(sigWinch)
	}

	// Set env vars — Setenv may be rejected by the server, that's ok
	session.Setenv("TERM", "xterm-256color")
	session.Setenv("SHELL", "/bin/bash")

	session.Stdin = os.Stdin
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	if err := session.Shell(); err != nil {
		return fmt.Errorf("starting shell: %w", err)
	}

	return session.Wait()
}

// Exec runs a command and returns the exit code.
func Exec(client *ssh.Client, command string) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	session.Stdin = os.Stdin
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	return session.Run(command)
}

// Download copies a remote file to a local writer.
func Download(client *ssh.Client, remotePath string, dst io.Writer) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	session.Stdout = dst
	return session.Run(fmt.Sprintf("cat %q", remotePath))
}

// Upload copies a local reader to a remote file.
func Upload(client *ssh.Client, src io.Reader, remotePath string, size int64) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	go func() {
		w, _ := session.StdinPipe()
		defer w.Close()
		io.Copy(w, src)
	}()

	return session.Run(fmt.Sprintf("cat > %q", remotePath))
}
