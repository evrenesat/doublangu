package library

import "fmt"

// Library groups works under a common language pair. Its identity and language
// fields must pass Validate before the record is used; the zero value is invalid.
type Library struct {
	ID             ULID   `json:"id"`
	Name           string `json:"name"`
	SourceLanguage string `json:"source_language"` // canonical BCP-47
	TargetLanguage string `json:"target_language"` // canonical BCP-47
	Description    string `json:"description"`
	CreatedAt      string `json:"created_at"` // ISO 8601 UTC
	UpdatedAt      string `json:"updated_at"` // ISO 8601 UTC
}

// Work represents an imported media work (audiobook, ebook, etc.) belonging
// to a library.
type Work struct {
	ID        ULID   `json:"id"`
	LibraryID ULID   `json:"library_id"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	Kind      string `json:"kind"`
	SourceURL string `json:"source_url"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Edition is a specific version or format of a work (e.g., a particular
// audiobook narrator edition, or a specific ePub release).
type Edition struct {
	ID        ULID   `json:"id"`
	WorkID    ULID   `json:"work_id"`
	Name      string `json:"name"`
	Language  string `json:"language"` // canonical BCP-47
	Format    string `json:"format"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Chapter is a timed segment of an edition. start_ms, end_ms, and
// duration_ms are integer milliseconds.
type Chapter struct {
	ID         ULID   `json:"id"`
	EditionID  ULID   `json:"edition_id"`
	Title      string `json:"title"`
	ChapterNum int    `json:"chapter_number"`
	StartMs    int64  `json:"start_ms"`
	EndMs      int64  `json:"end_ms"`
	DurationMs int64  `json:"duration_ms"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// SourceAsset records a source file or blob associated with a chapter.
// Timing uses integer milliseconds.
type SourceAsset struct {
	ID         ULID   `json:"id"`
	ChapterID  ULID   `json:"chapter_id"`
	URL        string `json:"url"`
	MIMEType   string `json:"mime_type"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256Hash string `json:"sha256_hash"`
	StartMs    int64  `json:"start_ms"`
	EndMs      int64  `json:"end_ms"`
	DurationMs int64  `json:"duration_ms"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// NewLibrary creates a library with a generated ID and canonical languages.
func NewLibrary(name, sourceLanguage, targetLanguage, description string) (Library, error) {
	record := Library{
		ID:             newULID(),
		Name:           name,
		SourceLanguage: sourceLanguage,
		TargetLanguage: targetLanguage,
		Description:    description,
	}
	if err := record.Validate(); err != nil {
		return Library{}, err
	}
	return record, nil
}

// Validate verifies the reusable Library representation boundary.
func (record *Library) Validate() error {
	id, err := validateRecordID("library.id", record.ID)
	if err != nil {
		return err
	}
	sourceLanguage, err := validateLanguage("library.source_language", record.SourceLanguage)
	if err != nil {
		return err
	}
	targetLanguage, err := validateLanguage("library.target_language", record.TargetLanguage)
	if err != nil {
		return err
	}
	record.ID = id
	record.SourceLanguage = sourceLanguage
	record.TargetLanguage = targetLanguage
	return nil
}

// NewWork creates a work with a generated ID after validating its library ID.
func NewWork(libraryID ULID, title, author, kind, sourceURL string) (Work, error) {
	record := Work{
		ID:        newULID(),
		LibraryID: libraryID,
		Title:     title,
		Author:    author,
		Kind:      kind,
		SourceURL: sourceURL,
	}
	if err := record.Validate(); err != nil {
		return Work{}, err
	}
	return record, nil
}

// Validate verifies the reusable Work representation boundary.
func (record *Work) Validate() error {
	id, err := validateRecordID("work.id", record.ID)
	if err != nil {
		return err
	}
	libraryID, err := validateRecordID("work.library_id", record.LibraryID)
	if err != nil {
		return err
	}
	record.ID = id
	record.LibraryID = libraryID
	return nil
}

// NewEdition creates an edition with a generated ID and canonical language.
func NewEdition(workID ULID, name, language, format string) (Edition, error) {
	record := Edition{
		ID:       newULID(),
		WorkID:   workID,
		Name:     name,
		Language: language,
		Format:   format,
	}
	if err := record.Validate(); err != nil {
		return Edition{}, err
	}
	return record, nil
}

// Validate verifies the reusable Edition representation boundary.
func (record *Edition) Validate() error {
	id, err := validateRecordID("edition.id", record.ID)
	if err != nil {
		return err
	}
	workID, err := validateRecordID("edition.work_id", record.WorkID)
	if err != nil {
		return err
	}
	language, err := validateLanguage("edition.language", record.Language)
	if err != nil {
		return err
	}
	record.ID = id
	record.WorkID = workID
	record.Language = language
	return nil
}

// NewChapter creates a chapter with a generated ID and validated timing.
func NewChapter(editionID ULID, title string, chapterNum int, startMs, endMs, durationMs int64) (Chapter, error) {
	record := Chapter{
		ID:         newULID(),
		EditionID:  editionID,
		Title:      title,
		ChapterNum: chapterNum,
		StartMs:    startMs,
		EndMs:      endMs,
		DurationMs: durationMs,
	}
	if err := record.Validate(); err != nil {
		return Chapter{}, err
	}
	return record, nil
}

// Validate verifies the reusable Chapter representation boundary.
func (record *Chapter) Validate() error {
	id, err := validateRecordID("chapter.id", record.ID)
	if err != nil {
		return err
	}
	editionID, err := validateRecordID("chapter.edition_id", record.EditionID)
	if err != nil {
		return err
	}
	if err := validateMilliseconds("chapter", record.StartMs, record.EndMs, record.DurationMs); err != nil {
		return err
	}
	record.ID = id
	record.EditionID = editionID
	return nil
}

// NewSourceAsset creates a source asset with a generated ID and validated timing.
func NewSourceAsset(chapterID ULID, url, mimeType string, sizeBytes int64, sha256Hash string, startMs, endMs, durationMs int64) (SourceAsset, error) {
	record := SourceAsset{
		ID:         newULID(),
		ChapterID:  chapterID,
		URL:        url,
		MIMEType:   mimeType,
		SizeBytes:  sizeBytes,
		SHA256Hash: sha256Hash,
		StartMs:    startMs,
		EndMs:      endMs,
		DurationMs: durationMs,
	}
	if err := record.Validate(); err != nil {
		return SourceAsset{}, err
	}
	return record, nil
}

// Validate verifies the reusable SourceAsset representation boundary.
func (record *SourceAsset) Validate() error {
	id, err := validateRecordID("source_asset.id", record.ID)
	if err != nil {
		return err
	}
	chapterID, err := validateRecordID("source_asset.chapter_id", record.ChapterID)
	if err != nil {
		return err
	}
	if err := validateMilliseconds("source_asset", record.StartMs, record.EndMs, record.DurationMs); err != nil {
		return err
	}
	record.ID = id
	record.ChapterID = chapterID
	return nil
}

func validateRecordID(field string, id ULID) (ULID, error) {
	parsed, err := ParseULID(id.String())
	if err != nil {
		return "", fmt.Errorf("%s: %w", field, err)
	}
	if parsed.IsZero() {
		return "", fmt.Errorf("%s: zero ULID", field)
	}
	return parsed, nil
}

func validateLanguage(field, value string) (string, error) {
	canonical, err := ParseBCP47(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", field, err)
	}
	return canonical, nil
}

func validateMilliseconds(record string, startMs, endMs, durationMs int64) error {
	if startMs < 0 {
		return fmt.Errorf("%s.start_ms: must be non-negative", record)
	}
	if endMs < 0 {
		return fmt.Errorf("%s.end_ms: must be non-negative", record)
	}
	if durationMs < 0 {
		return fmt.Errorf("%s.duration_ms: must be non-negative", record)
	}
	if endMs < startMs {
		return fmt.Errorf("%s.end_ms: must be greater than or equal to start_ms", record)
	}
	return nil
}
