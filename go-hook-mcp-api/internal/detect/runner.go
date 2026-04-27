package detect

import (
	"context"
	"sync"
)

// Run executes all detectors in parallel and returns aggregated findings and errors.
func Run(ctx context.Context, detectors []Detector) ([]Finding, []error) {
	if len(detectors) == 0 {
		return nil, nil
	}
	type result struct {
		findings []Finding
		err      error
	}
	results := make([]result, len(detectors))
	var wg sync.WaitGroup
	for i, d := range detectors {
		wg.Add(1)
		go func(i int, d Detector) {
			defer wg.Done()
			f, err := d.Detect(ctx)
			results[i] = result{f, err}
		}(i, d)
	}
	wg.Wait()

	var allFindings []Finding
	var allErrors []error
	for _, r := range results {
		allFindings = append(allFindings, r.findings...)
		if r.err != nil {
			allErrors = append(allErrors, r.err)
		}
	}
	return allFindings, allErrors
}
