package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// RunDAGStep executes one step: Build → client.Complete → Parse. On a parse
// failure it retries the call exactly once before giving up, mirroring the
// behaviour of CompleteJSON for individual PromptBuilder paths.
func RunDAGStep(ctx context.Context, step StepDef, inputs StepInputs, client Client) (any, error) {
	req := step.Build(inputs)

	var lastErr error
	for range 2 {
		resp, err := client.Complete(ctx, string(step.OutputKind), req)
		if err != nil {
			return nil, err
		}
		out, err := step.Parse(resp.Text)
		if err != nil {
			lastErr = fmt.Errorf("dag: step %q parse error: %w", step.ID, err)
			continue
		}
		return out, nil
	}
	return nil, lastErr
}

// RunDAG executes a validated DAG in wave order: steps whose DepStep
// dependencies are all satisfied run in parallel. The function returns a map
// from stepID to the parsed output of each step.
//
// Cycle detection: if after a wave no new steps became runnable and there are
// still pending steps, a cycle is reported.
func RunDAG(ctx context.Context, dag GenerationDAG, inputs StepInputs, client Client) (map[string]any, error) {
	results := make(map[string]any)

	// Copy the step list so we can track which are still pending.
	pending := make([]StepDef, len(dag.Steps))
	copy(pending, dag.Steps)

	for len(pending) > 0 {
		// Find all steps whose DepStep dependencies are satisfied.
		var wave []StepDef
		var next []StepDef
		for _, s := range pending {
			if stepDepsReady(s, results) {
				wave = append(wave, s)
			} else {
				next = append(next, s)
			}
		}

		if len(wave) == 0 {
			// Nothing progressed — cycle or unresolvable dependency.
			ids := make([]string, len(pending))
			for i, s := range pending {
				ids[i] = s.ID
			}
			return nil, fmt.Errorf("dag: cycle or unresolvable dependency among steps: %v", ids)
		}

		// Build per-step inputs with the current results snapshot.
		waveInputs := inputs
		waveInputs.Steps = make(map[string]any, len(results))
		for k, v := range results {
			waveInputs.Steps[k] = v
		}

		// Execute the wave in parallel.
		type result struct {
			id  string
			out any
			err error
		}
		ch := make(chan result, len(wave))
		var wg sync.WaitGroup
		for _, s := range wave {
			wg.Add(1)
			go func(s StepDef) {
				defer wg.Done()
				out, err := RunDAGStep(ctx, s, waveInputs, client)
				ch <- result{id: s.ID, out: out, err: err}
			}(s)
		}
		wg.Wait()
		close(ch)

		for r := range ch {
			if r.err != nil {
				return nil, r.err
			}
			results[r.id] = r.out
		}

		pending = next
	}

	return results, nil
}

// stepDepsReady reports whether all DepStep deps for s are present in results.
// Non-DepStep deps (data deps) are always considered satisfied at this layer.
func stepDepsReady(s StepDef, results map[string]any) bool {
	for _, dep := range s.Deps {
		if dep.Kind == DepStep {
			if _, ok := results[dep.StepID]; !ok {
				return false
			}
		}
	}
	return true
}

// ParseMapResult is a shared Parse implementation for task steps that expect
// a non-empty JSON object. It is exported so language-plugin DAG steps can
// reuse it without duplicating the ExtractJSON + empty-check logic.
func ParseMapResult(raw string) (any, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(ExtractJSON(raw)), &m); err != nil {
		return nil, fmt.Errorf("parse map result: %w", err)
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("parse map result: empty object")
	}
	return m, nil
}
