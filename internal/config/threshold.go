package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Threshold is written as "90%", "90%/5m" or "2.0/5m": a limit, optionally
// sustained for a duration before it counts as breached.
type Threshold struct {
	Value float64
	For   time.Duration
}

func ParseThreshold(s string) (Threshold, error) {
	var t Threshold
	s = strings.TrimSpace(s)
	valuePart, forPart, hasFor := strings.Cut(s, "/")
	if hasFor {
		d, err := time.ParseDuration(strings.TrimSpace(forPart))
		if err != nil {
			return t, fmt.Errorf("threshold %q: bad duration: %w", s, err)
		}
		if d < 0 {
			return t, fmt.Errorf("threshold %q: duration must not be negative", s)
		}
		t.For = d
	}
	valuePart = strings.TrimSuffix(strings.TrimSpace(valuePart), "%")
	v, err := strconv.ParseFloat(valuePart, 64)
	if err != nil {
		return t, fmt.Errorf("threshold %q: bad value: %w", s, err)
	}
	if v <= 0 {
		return t, fmt.Errorf("threshold %q: value must be positive", s)
	}
	t.Value = v
	return t, nil
}

func (t *Threshold) UnmarshalYAML(n *yaml.Node) error {
	parsed, err := ParseThreshold(n.Value)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

func (t Threshold) String() string {
	if t.For == 0 {
		return strconv.FormatFloat(t.Value, 'f', -1, 64)
	}
	return fmt.Sprintf("%s for %s", strconv.FormatFloat(t.Value, 'f', -1, 64), t.For)
}
