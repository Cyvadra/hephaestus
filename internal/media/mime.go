// Package media classifies attachment media types. It is a leaf package so
// that ingest, persistence and the LLM client can agree on what counts as
// visual input without depending on one another.
package media

// SupportedVisual reports whether a MIME type can be sent to the model as
// visual input.
func SupportedVisual(mediaType string) bool {
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
