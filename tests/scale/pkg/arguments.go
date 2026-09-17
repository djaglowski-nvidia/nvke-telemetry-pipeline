package runner

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	// argumentNamePattern defines valid names for flags and argument references.
	argumentNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
	// runIDPattern limits run IDs to 1-63 ASCII characters with alphanumeric ends.
	runIDPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_.-]{0,61}[A-Za-z0-9])?$`)
)

// ParseArguments parses repeated NAME=VALUE flag values into a fresh map.
// It generates run-id when omitted and preserves an explicitly supplied ID.
// Names must start with an ASCII letter and contain only letters, digits,
// underscores or hyphens. Duplicate assignments and an empty run-id are errors.
// Call it once per invocation before parsing the plan.
func ParseArguments(values []string) (map[string]string, error) {
	args := make(map[string]string, len(values)+1)
	for _, assignment := range values {
		name, value, ok := strings.Cut(assignment, "=")
		if !ok || !argumentNamePattern.MatchString(name) {
			return nil, errors.New("argument must have the form NAME=VALUE with a valid name")
		}
		if _, exists := args[name]; exists {
			return nil, fmt.Errorf("duplicate argument %q", name)
		}
		args[name] = value
	}
	if _, supplied := args["run-id"]; !supplied {
		args["run-id"] = rand.Text()
	}
	if err := validateRunID(args["run-id"]); err != nil {
		return nil, err
	}
	return args, nil
}

// validateRunID rejects IDs that do not match the shared attribution format.
func validateRunID(id string) error {
	if !runIDPattern.MatchString(id) {
		return errors.New("run-id must be 1-63 ASCII characters, start and end with a letter or digit, and contain only letters, digits, underscores, hyphens or dots")
	}
	return nil
}
