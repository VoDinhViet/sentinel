// Package terminal is how the commands ask a person for a password or a yes. The password is typed, or
// piped in with --password-stdin; it is never an argument or an environment variable, which other users can read.
package terminal

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Terminal is the input and output of a command. The zero value is not useful: use Std, or fill the
// fields in a test.
type Terminal struct {
	In       io.Reader
	Out      io.Writer
	FD       int
	IsTTY    bool
	ReadPass func(fd int) ([]byte, error)

	reader *bufio.Reader // one for every line read, so a piped input is not swallowed by the first read
}

func (t *Terminal) lines() *bufio.Reader {
	if t.reader == nil {
		t.reader = bufio.NewReader(t.In)
	}
	return t.reader
}

// Std is the terminal of the process: prompts go to stderr, so stdout stays for the output of the command.
func Std() Terminal {
	fd := int(os.Stdin.Fd())
	return Terminal{In: os.Stdin, Out: os.Stderr, FD: fd, IsTTY: term.IsTerminal(fd), ReadPass: term.ReadPassword}
}

// Password asks for a password without showing it. With confirm it asks twice and the two must match.
// With fromStdin it reads one line from the input instead (for scripts).
func (t *Terminal) Password(prompt string, confirm, fromStdin bool) (string, error) {
	if fromStdin {
		line, err := t.lines().ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("đọc mật khẩu: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return "", errors.New("không đọc được mật khẩu từ stdin")
		}
		return line, nil
	}
	if !t.IsTTY {
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

func (t *Terminal) ask(prompt string) (string, error) {
	fmt.Fprint(t.Out, prompt)
	typed, err := t.ReadPass(t.FD)
	fmt.Fprintln(t.Out)
	if err != nil {
		return "", fmt.Errorf("đọc mật khẩu: %w", err)
	}
	return string(typed), nil
}

// Line prints a prompt and reads one line of the input, without its line end. It is for a question that
// is answered by typing something, such as the name of what is about to be deleted.
func (t *Terminal) Line(prompt string) (string, error) {
	fmt.Fprint(t.Out, prompt)
	line, err := t.lines().ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("đọc câu trả lời: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
