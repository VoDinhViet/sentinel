package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-playground/validator/v10"
)

// fieldValidator checks the rules that belong to one field, written as `validate:"..."` tags on the
// types. Rules that look at the whole file (a job's repository exists, names are unique, a schedule
// parses) are in validate.go. It is built once because the validator caches what it learns about
// each type.
var fieldValidator = newFieldValidator()

func newFieldValidator() *validator.Validate {
	v := validator.New()
	// Name a field by its YAML key, so an error reads like the file the user wrote.
	v.RegisterTagNameFunc(func(field reflect.StructField) string {
		key, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		if key == "-" {
			return ""
		}
		return key
	})
	for tag, check := range map[string]validator.Func{
		"https_url": func(fl validator.FieldLevel) bool { return isHTTPSOrRef(fl.Field().String()) },
		"abspath":   func(fl validator.FieldLevel) bool { return filepath.IsAbs(fl.Field().String()) },
	} {
		if err := v.RegisterValidation(tag, check); err != nil {
			panic(err) // the tag names above are fixed, so this is a bug in this file
		}
	}
	return v
}

// isHTTPSOrRef accepts an https:// URL, or a value that is still a ${VAR} reference (the config editor
// reads files without expanding them): it is checked once it has a value. A webhook or heartbeat URL
// often is the secret itself, so it must not travel over plain http.
func isHTTPSOrRef(url string) bool {
	return strings.Contains(url, "${") || strings.HasPrefix(url, "https://")
}

// fieldErrors runs the tag rules over the whole config and words each failure.
func fieldErrors(c *Config) []error {
	err := fieldValidator.Struct(c)
	if err == nil {
		return nil
	}
	var failures validator.ValidationErrors
	if !errors.As(err, &failures) {
		return []error{err}
	}
	errs := make([]error, 0, len(failures))
	for _, failure := range failures {
		where, field := c.fieldPlace(strings.TrimPrefix(failure.Namespace(), "Config."))
		text := fieldMessage(failure)
		if field != "" {
			text = field + " " + text
		}
		errs = append(errs, errors.New(where+": "+text))
	}
	return errs
}

var (
	entryPath = regexp.MustCompile(`^(jobs|uptime)\[(\d+)\]\.(.+)$`)
	repoPath  = regexp.MustCompile(`^repositories\[(.*?)\]\.(.+)$`)
)

// fieldPlace splits the path of a failed field into where it is and the field itself:
// jobs[0].mysql.database into `jobs[0] "db"` and mysql.database, api.token into api and token.
// A field with nothing around it (shutdown_wait) is its own place.
func (c *Config) fieldPlace(path string) (where, field string) {
	if match := entryPath.FindStringSubmatch(path); match != nil {
		i, _ := strconv.Atoi(match[2])
		return c.entryWhere(match[1], i), match[3]
	}
	if match := repoPath.FindStringSubmatch(path); match != nil {
		return fmt.Sprintf("repository %q", match[1]), match[2]
	}
	if dot := strings.LastIndex(path, "."); dot >= 0 {
		return path[:dot], path[dot+1:]
	}
	return path, ""
}

// entryWhere names a job or an uptime check by position and name, `jobs[0] "db"`; the name is left
// out when the entry has none.
func (c *Config) entryWhere(section string, i int) string {
	name := ""
	switch {
	case section == "jobs" && i < len(c.Jobs):
		name = c.Jobs[i].Name
	case section == "uptime" && i < len(c.UptimeChecks):
		name = c.UptimeChecks[i].Name
	}
	if name == "" {
		return fmt.Sprintf("%s[%d]", section, i)
	}
	return fmt.Sprintf("%s[%d] %q", section, i, name)
}

// fieldMessage says what is wrong with a field, to follow its name: "is required", "must not be negative".
func fieldMessage(failure validator.FieldError) string {
	param := failure.Param()
	switch failure.Tag() {
	case "required":
		return "is required"
	case "required_if": // "Type mysql": the field the rule looks at, then its value
		field, value, _ := strings.Cut(param, " ")
		return fmt.Sprintf("is required when %s is %s", strings.ToLower(field), value)
	case "required_with":
		return fmt.Sprintf("is required when %s is set", yamlKeyOf(param))
	case "oneof":
		return "must be " + orList(strings.Fields(param))
	case "gte":
		if param == "0" {
			return "must not be negative"
		}
		return "must be at least " + param
	case "min":
		if failure.Kind() == reflect.Slice {
			return "needs at least " + param + " entry"
		}
		return "must be at least " + param
	case "lte":
		return "must be at most " + param
	case "https_url":
		return "must start with https://"
	case "abspath":
		return fmt.Sprintf("must be an absolute path, got %q", failure.Value())
	}
	return fmt.Sprintf("fails the %q rule", failure.Tag())
}

// yamlKeyOf is the key a validator parameter names in the file: the name of a Go field, as
// "TLSKey", means "tls_key" there.
func yamlKeyOf(goField string) string {
	switch goField {
	case "TLSCert":
		return "tls_cert"
	case "TLSKey":
		return "tls_key"
	}
	return strings.ToLower(goField)
}

// orList joins choices as "a, b or c".
func orList(choices []string) string {
	if len(choices) < 2 {
		return strings.Join(choices, "")
	}
	return strings.Join(choices[:len(choices)-1], ", ") + " or " + choices[len(choices)-1]
}
