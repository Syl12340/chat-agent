package courseware

// Extraction records how much of a document the text projection actually covers.
// It exists because a non-empty result proves only that something was read, not
// that everything was: a PDF that mixes a readable page with font-encoded text
// used to look identical to a complete extraction, and a student would then be
// taught from a lecture that had silently lost half of itself.
type Extraction struct {
	// Status is "complete", "limited" or "failed".
	Status string `json:"extraction_status"`
	// Warnings say what is missing, in the reader's language, so the model can
	// tell the user the material is incomplete instead of trusting it.
	Warnings []string `json:"extraction_warnings,omitempty"`
	// Pages is what the file itself claims, not the order this reader visited
	// streams in. Zero means the format does not carry page numbers.
	Pages int `json:"extraction_pages,omitempty"`
}

const (
	extractionComplete = "complete"
	extractionLimited  = "limited"
	extractionFailed   = "failed"
)

// normalizeStatus fills in the status of records written before this field
// existed. They were only stored when extraction succeeded, so treating them as
// complete is what the old code already assumed.
func normalizeStatus(status string) string {
	if status == "" {
		return extractionComplete
	}
	return status
}

// Status reports what the text projection covers. Records written before the field
// existed were only stored on success, so they read as complete.
func (d Document) Status() string { return normalizeStatus(d.Extraction) }

// Incomplete reports whether the text is known to be missing material.
func (d Document) Incomplete() bool { return d.Status() != extractionComplete }

func (e Extraction) valid() bool {
	switch e.Status {
	case "", extractionComplete, extractionLimited, extractionFailed:
		return true
	}
	return false
}
