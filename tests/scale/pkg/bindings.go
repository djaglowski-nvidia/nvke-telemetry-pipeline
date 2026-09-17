package runner

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// argumentReferencePattern matches escaped openings before complete expressions
// and unclosed openings so literal delimiters take precedence.
var argumentReferencePattern = regexp.MustCompile(`(?s)\{\{\{\{|\{\{.*?\}\}|\{\{`)

// bindArguments replaces references in raw text before YAML decoding. Replacement
// values are not escaped or scanned again, including when they contain $ or {{.
func bindArguments(data string, args map[string]string) (string, error) {
	var errs []error
	missing := make(map[string]bool)
	resolved := argumentReferencePattern.ReplaceAllStringFunc(data, func(reference string) string {
		switch reference {
		case "{{{{":
			return "{{"
		case "{{":
			errs = append(errs, errors.New("unterminated argument reference"))
			return reference
		}
		expression := strings.TrimSpace(reference[2 : len(reference)-2])
		name, ok := strings.CutPrefix(expression, "args.")
		if !ok || !argumentNamePattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("unsupported argument reference %q", expression))
			return reference
		}
		value, ok := args[name]
		if !ok {
			missing[name] = true
		}
		return value
	})
	if len(missing) != 0 {
		names := make([]string, 0, len(missing))
		for name := range missing {
			names = append(names, name)
		}
		sort.Strings(names)
		errs = append(errs, fmt.Errorf("missing arguments: %s", strings.Join(names, ", ")))
	}
	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	return resolved, nil
}
