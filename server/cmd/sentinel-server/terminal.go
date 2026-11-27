package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// terminal is how a command asks for a password. The password is typed, or piped in with
// --password-stdin; it is never an argument or an environment variable, which other users can read.
type terminal struct {
	in       io.Reader
	out      io.Writer
	fd       int
	isTTY    bool
	readPass func(fd int) ([]byte, error)
}

func stdTerminal() terminal {
	fd := int(os.Stdin.Fd())
	return terminal{in: os.Stdin, out: os.Stderr, fd: fd, isTTY: term.IsTerminal(fd), readPass: term.ReadPassword}
}

// password asks for a password without showing it. With confirm it asks twice and the two must match.
// With fromStdin it reads one line from the input instead (for scripts).
func (t terminal) password(prompt string, confirm, fromStdin bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(t.in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("đọc mật khẩu: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return "", errors.New("không đọc được mật khẩu từ stdin")
		}
		return line, nil
	}
	if !t.isTTY {
		return "", errors.New("không có terminal để nhập mật khẩu: dùng --password-stdin")
	}
	first, err := t.ask(prompt)
	if err != nil {
		return "", err
	}
	if !confirm {
		return first, nil
	}
	second, err := t.ask("Nhập lại mật khẩu: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("hai lần nhập không giống nhau")
	}
	return first, nil
}

func (t terminal) ask(prompt string) (string, error) {
	fmt.Fprint(t.out, prompt)
	typed, err := t.readPass(t.fd)
	fmt.Fprintln(t.out)
	if err != nil {
		return "", fmt.Errorf("đọc mật khẩu: %w", err)
	}
	return string(typed), nil
}
