package v1

// CapabilityID identifies a well-known plugin capability. The core uses these
// constants to dispatch import, processing, and rendering work to registered
// plugins.
type CapabilityID string

// Capability IDs for the first language pair (Dutch→English). Every capability
// below corresponds to a documented plugin surface; additions require a new API
// version.
const (
	// Import ingests external content into the library. Accepts audiobooks
	// (M4B chapters), audio files, ebooks (EPUB), text (TXT, Markdown, HTML),
	// text-layer PDF, pasted text, and safe HTTP(S) URLs.
	CapImport CapabilityID = "import"

	// CapProbe inspects raw media and returns detected properties (format,
	// codec, duration, language hints) without modifying the original.
	CapProbe CapabilityID = "probe"

	// CapExtraction extracts text or structured content from container formats
	// (EPUB, M4B chapters, PDF text layer).
	CapExtraction CapabilityID = "extraction"

	// CapSegmentation splits text into sentences with offsets. Output is an
	// ordered non-overlapping sequence of sentence spans.
	CapSegmentation CapabilityID = "segmentation"

	// CapSTT (speech-to-text) transcribes audio into timed sentence-level text.
	CapSTT CapabilityID = "stt"

	// CapAlignment matches supplied text against audio and produces word-level
	// timings with confidence scores.
	CapAlignment CapabilityID = "alignment"

	// CapTranslation translates text from a source language to a target
	// language, preserving sentence-level correspondence.
	CapTranslation CapabilityID = "translation"

	// CapAnalysis produces explanations, construction detection, multiword
	// expressions, and contrasting examples for language-learning content.
	CapAnalysis CapabilityID = "analysis"

	// CapTTS (text-to-speech) synthesises audio from text with the given
	// language, voice, and speed parameters.
	CapTTS CapabilityID = "tts"

	// CapLessonRendering composes passive-listening lessons from transcript,
	// translation, explanation, and TTS segments.
	CapLessonRendering CapabilityID = "lesson_rendering"

	// CapExport renders content into portable formats (offline packages,
	// downloadable audio, printable views).
	CapExport CapabilityID = "export"
)

// ValidCapabilities is the set of capability IDs defined by this API version.
var ValidCapabilities = map[CapabilityID]bool{
	CapImport:          true,
	CapProbe:           true,
	CapExtraction:      true,
	CapSegmentation:    true,
	CapSTT:             true,
	CapAlignment:       true,
	CapTranslation:     true,
	CapAnalysis:        true,
	CapTTS:             true,
	CapLessonRendering: true,
	CapExport:          true,
}

// IsValid reports whether c is a capability defined by this API version.
func (c CapabilityID) IsValid() bool {
	return ValidCapabilities[c]
}
