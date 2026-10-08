package measurements

import (
	"fmt"
	"time"

	"github.com/prometheus/common/model"
)

// CheckReport retains the hold duration and the series that caused failure.
// A nil FiredAt means the check did not fire during collection.
type CheckReport struct {
	For     time.Duration  `json:"for"`
	FiredAt *time.Time     `json:"fired_at,omitempty"`
	Series  []model.Metric `json:"series,omitempty"`
}

// checkState tracks each series independently within one measurement run.
type checkState struct {
	// hold is the required duration across consecutive matching evaluations.
	hold time.Duration
	// pending maps complete label sets to their first matching evaluation.
	pending map[string]time.Time
}

// evaluate returns firing series and resets timers for series no longer present.
// Vector membership signals a violation, regardless of the sample's value.
func (s *checkState) evaluate(value model.Value, at time.Time) ([]model.Metric, error) {
	vector, ok := value.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("check expression must return an instant vector, got %T", value)
	}
	current := make(map[string]time.Time, len(vector))
	var fired []model.Metric
	for _, sample := range vector {
		labels := sample.Metric.String()
		since, ok := s.pending[labels]
		if !ok {
			since = at
		}
		current[labels] = since
		if at.Sub(since) >= s.hold {
			fired = append(fired, sample.Metric)
		}
	}
	s.pending = current
	return fired, nil
}
