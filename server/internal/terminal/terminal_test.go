package terminal

import (
	"io"
	"strings"
	"testing"
)

func TestPasswordPromptAsksTwiceAndPointsToStdinWithoutATerminal(t *testing.T) {
	answers := []string{"first password!", "first password!"}
	typed := &Terminal{IsTTY: true, Out: io.Discard, ReadPass: func(int) ([]byte, error) {
		answer := answers[0]
		answers = answers[1:]
		return []byte(answer), nil
	}}
	if got, err := typed.Password("p: ", true, false); err != nil || got != "first password!" {
		t.Errorf("typed twice: %q, %v", got, err)
	}
	answers = []string{"one thing here", "another thing"}
	if _, err := typed.Password("p: ", true, false); err == nil {
		t.Error("two different entries were accepted")
	}
	piped := &Terminal{In: strings.NewReader("from a pipe\n"), Out: io.Discard}
	if _, err := piped.Password("p: ", true, false); err == nil || !strings.Contains(err.Error(), "--password-stdin") {
		t.Errorf("no terminal and no flag: err = %v, want a hint about --password-stdin", err)
	}
	if got, err := piped.Password("p: ", true, true); err != nil || got != "from a pipe" {
		t.Errorf("stdin: %q, %v", got, err)
	}
	if _, err := (&Terminal{In: strings.NewReader(""), Out: io.Discard}).Password("p", false, true); err == nil {
		t.Error("an empty stdin gave a password")
	}
}

func TestLineReadsOneLineWithoutItsEnd(t *testing.T) {
	typed := &Terminal{In: strings.NewReader("web01\r\nnext\n"), Out: io.Discard}
	if got, err := typed.Line("name: "); err != nil || got != "web01" {
		t.Errorf("got %q, %v", got, err)
	}
	if got, err := typed.Line("name: "); err != nil || got != "next" {
		t.Errorf("the second line was lost: %q, %v", got, err)
	}
	if got, err := (&Terminal{In: strings.NewReader(""), Out: io.Discard}).Line("name: "); err != nil || got != "" {
		t.Errorf("empty input: %q, %v", got, err)
	}
}
