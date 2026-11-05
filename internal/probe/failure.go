package probe

import "fmt"

// ModelError carries host-defined categories, never provider bodies, prompts,
// response text or hidden reasoning. Cause preserves cancellation semantics.
type ModelError struct {
	Kind       string
	HTTPStatus int
	cause      error
}

func (e *ModelError) Error() string {
	if e.Kind == "http" {
		return fmt.Sprintf("model endpoint returned HTTP %d", e.HTTPStatus)
	}
	return "model request failed: " + e.Kind
}
func (e *ModelError) Unwrap() error { return e.cause }
