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

func runInteractive(session *ssh.Session, echo uint32, start func() error) error {
	fd := int(os.Stdin.Fd())
	isTerminal := term.IsTerminal(fd)
	if isTerminal {
		oldState, err := term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("setting raw terminal: %w", err)
		}
		defer resetLocalTerminal(fd, oldState)

		w, h, _ := term.GetSize(fd)
		if err := session.RequestPty("xterm-256color", h, w, ssh.TerminalModes{
			ssh.ECHO:          echo,
			ssh.TTY_OP_ISPEED: 14400,
			ssh.TTY_OP_OSPEED: 14400,
		}); err != nil {
			return fmt.Errorf("requesting PTY: %w", err)
		}

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

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("opening session stdin: %w", err)
	}

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		_, _ = io.Copy(stdin, os.Stdin)
		_ = stdin.Close()
	}()

	if err := start(); err != nil {
		return err
	}

	err = session.Wait()
	<-copyDone
	return err
}

func resetLocalTerminal(fd int, oldState *term.State) {
	if oldState != nil {
		_ = term.Restore(fd, oldState)
	}

	// Undo common interactive modes without clearing the user's screen:
	// reset attributes, show cursor, leave alt-screen, disable bracketed paste,
	// disable mouse tracking, and re-enable line wrap.
	_, _ = os.Stdout.WriteString(
		"\x1b[0m" + // normal attributes
			"\x1b[?25h" + // show cursor
			"\x1b[?1049l" + // leave alt screen
			"\x1b[?2004l" + // disable bracketed paste
			"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l" + // disable mouse modes
			"\x1b[?7h", // line wrap on
	)
}

// Connect establishes an SSH client connection over an existing net.Conn
// (typically a WebSocket). Returns the ssh.Client ready for use.
func Connect(conn net.Conn) (*ssh.Client, error) {
	return connect(conn, "cloudchamber", config.PrivateKeyPath())
}

// ConnectVM establishes an SSH client connection directly to a VM over an existing net.Conn.
func ConnectVM(conn net.Conn) (*ssh.Client, error) {
	return connect(conn, "root", config.PrivateKeyPath())
}

func connect(conn net.Conn, user, keyPath string) (*ssh.Client, error) {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User: user,
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

	// Set env vars — Setenv may be rejected by the server, that's ok
	session.Setenv("TERM", "xterm-256color")
	session.Setenv("SHELL", "/bin/bash")

	return runInteractive(session, 1, func() error {
		if err := session.Shell(); err != nil {
			return fmt.Errorf("starting shell: %w", err)
		}
		return nil
	})
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

// ExecInteractive runs a command with a PTY attached when stdin is a terminal.
func ExecInteractive(client *ssh.Client, command string) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	return runInteractive(session, 0, func() error {
		return session.Start(command)
	})
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
