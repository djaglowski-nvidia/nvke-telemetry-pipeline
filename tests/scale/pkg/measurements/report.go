// Package measurements provides observations and findings for scale test plans.
package measurements

// Report contains measurement-owned findings. A nil or empty map is valid.
// The executor does not interpret its contents. The measurement must stop
// modifying the map and its values before returning it.
type Report map[string]any
