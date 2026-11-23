package service

import "fmt"

// InvalidError is a problem with what the caller submitted. Its message is
// written for the person using the UI and is safe to show; every other error
// is internal and must only be logged.
type InvalidError struct{ msg string }

func (e InvalidError) Error() string { return e.msg }

// Invalid returns an InvalidError with a formatted, user-facing message.
func Invalid(format string, args ...any) error {
	return InvalidError{msg: fmt.Sprintf(format, args...)}
}
