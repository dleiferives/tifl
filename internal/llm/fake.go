package llm

import "context"

// Minimal valid JSON bodies for common builder kinds, exported so test helpers
// in different packages can share them and stay in sync when response shapes change.
const (
	FakeStoryJSON   = `{"story":"a a a","estimated_coverage":0.9,"glossary":[]}`
	FakeScopeOKJSON = `{"viable":true,"reason":"ok","suggested_topic":""}`
	FakeTaskJSON    = `{"question":"q","options":["x","y"],"correct_index":1,"sentence":"the ___ ran","acceptable_forms":["x"]}`
)

// FakeClient is a deterministic Client for unit tests anywhere in the tree: no
// network, no gateway, no credentials. Set Response (and optionally Err) for a
// fixed reply, or set Func for per-call control. Every call is recorded in Calls
// so tests can assert on the kind and request the code under test produced.
//
//	c := &llm.FakeClient{Response: llm.LLMResponse{Text: `{"ok":true}`}}
type FakeClient struct {
	Response LLMResponse
	Err      error
	Func     func(ctx context.Context, kind string, req LLMRequest) (LLMResponse, error)
	Calls    []FakeCall
}

// FakeCall is one recorded invocation of FakeClient.Complete.
type FakeCall struct {
	Kind string
	Req  LLMRequest
}

// compile-time assertion that we satisfy the interface.
var _ Client = (*FakeClient)(nil)

// Complete records the call and returns the configured response (or Func result).
func (f *FakeClient) Complete(ctx context.Context, kind string, req LLMRequest) (LLMResponse, error) {
	f.Calls = append(f.Calls, FakeCall{Kind: kind, Req: req})
	if f.Func != nil {
		return f.Func(ctx, kind, req)
	}
	return f.Response, f.Err
}
