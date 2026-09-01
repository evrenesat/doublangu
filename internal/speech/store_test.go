package speech

import (
	"context"
	"database/sql"
	"testing"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/store"
)

func seedSpeechArticle(t *testing.T, db *store.DB, articleID, blockID, sentenceID, occurrenceID, spanID, text string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO article (id, title, source_language, target_language, enrichment_status) VALUES (?, 'Speech test', 'nl', 'en', 'draft')`, articleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_block (id, article_id, block_index, kind, source_text) VALUES (?, ?, 0, 'paragraph', ?)`, blockID, articleID, text); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_sentence (id, article_block_id, sentence_index, start_utf16, end_utf16, source_text, source_hash) VALUES (?, ?, 0, 0, ?, ?, 'source-hash')`, sentenceID, blockID, len([]rune(text)), text); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_occurrence (id, article_block_id, article_sentence_id, kind, role, shadow_policy, shadow_text, confidence_milli) VALUES (?, ?, ?, 'word', 'token', 'token', 'word', 900)`, occurrenceID, blockID, sentenceID); err != nil {
		t.Fatal(err)
	}
	start := 0
	if len(text) > 3 {
		start = 0
	}
	if _, err := db.Exec(ctx, `INSERT INTO article_occurrence_span (id, article_occurrence_id, span_index, start_utf16, end_utf16, source_text) VALUES (?, ?, 0, ?, ?, ?)`, spanID, occurrenceID, start, len([]rune(text)), text); err != nil {
		t.Fatal(err)
	}
}

func TestQueueAudioDeduplicatesUnitsAndPrioritizesFirstSentence(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articleOne := "01J00000000000000000000010"
	articleTwo := "01J00000000000000000000020"
	seedSpeechArticle(t, db, articleOne, "01J00000000000000000000011", "01J00000000000000000000012", "01J00000000000000000000013", "01J00000000000000000000014", "bank")
	seedSpeechArticle(t, db, articleTwo, "01J00000000000000000000021", "01J00000000000000000000022", "01J00000000000000000000023", "01J00000000000000000000024", "bank")
	s := NewStore(db)
	if err := s.QueueArticleAudio(ctx, library.ULID(articleOne), false); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueArticleAudio(ctx, library.ULID(articleTwo), false); err != nil {
		t.Fatal(err)
	}
	var units, renders, jobsCount, sentenceBindings, occurrenceBindings int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM speech_unit`).Scan(&units); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audio_render`).Scan(&renders); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE execution_target = 'macos'`).Scan(&jobsCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_sentence_audio`).Scan(&sentenceBindings); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_occurrence_audio`).Scan(&occurrenceBindings); err != nil {
		t.Fatal(err)
	}
	if units != 2 || renders != 2 || jobsCount != 2 || sentenceBindings != 2 || occurrenceBindings != 2 {
		t.Fatalf("dedup counts units/renders/jobs/sentences/occurrences = %d/%d/%d/%d/%d", units, renders, jobsCount, sentenceBindings, occurrenceBindings)
	}
	var lexicalRenderID, narrationRenderID string
	if err := db.QueryRow(ctx, `SELECT id FROM audio_render WHERE retention_class = 'lexical_permanent'`).Scan(&lexicalRenderID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT audio_render_id FROM article_sentence_audio WHERE article_id = ?`, articleOne).Scan(&narrationRenderID); err != nil {
		t.Fatal(err)
	}
	if lexicalRenderID == narrationRenderID {
		t.Fatal("lexical and narration renders unexpectedly share an identity")
	}
	var lexicalJobs, narrationPriority int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE owner_id = ?`, lexicalRenderID).Scan(&lexicalJobs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT priority FROM job WHERE owner_id = ?`, narrationRenderID).Scan(&narrationPriority); err != nil {
		t.Fatal(err)
	}
	if lexicalJobs != 1 || narrationPriority != 70 {
		t.Fatalf("job ownership/priority = %d/%d", lexicalJobs, narrationPriority)
	}

	narration, err := s.GetNarration(ctx, library.ULID(articleOne))
	if err != nil {
		t.Fatal(err)
	}
	if narration.SentenceCount != 1 || narration.ReadyCount != 0 || narration.Status != NarrationQueued {
		t.Fatalf("narration before render = %+v", narration)
	}
}

func TestTerminalSpeechLeaseExpiryFailsRenderAndNarrationAfterThreeAttempts(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articleID := "01J00000000000000000000150"
	seedSpeechArticle(t, db, articleID, "01J00000000000000000000151", "01J00000000000000000000152", "01J00000000000000000000153", "01J00000000000000000000154", "zin")

	speechStore := NewStore(db)
	if err := speechStore.QueueArticleAudio(ctx, library.ULID(articleID), false); err != nil {
		t.Fatal(err)
	}
	var jobID, renderID string
	if err := db.QueryRow(ctx, `
		SELECT j.id, r.id
		FROM job j JOIN audio_render r ON r.id = j.owner_id
		JOIN article_sentence_audio a ON a.audio_render_id = r.id
		WHERE j.job_type = ? AND j.owner_type = 'audio_render' AND a.article_id = ?
	`, jobs.ChatterboxJobType, articleID).Scan(&jobID, &renderID); err != nil {
		t.Fatal(err)
	}
	var status, errorCode string
	if err := db.QueryRow(ctx, `SELECT narration_status, narration_error_code FROM article WHERE id = ?`, articleID).Scan(&status, &errorCode); err != nil {
		t.Fatal(err)
	}
	if status != NarrationQueued || errorCode != "" {
		t.Fatalf("initial narration state = %q/%q", status, errorCode)
	}

	jobStore := jobs.NewStore(db, speechStore.ReconcileTerminalJobTx)
	for expectedAttempt := 1; expectedAttempt <= 3; expectedAttempt++ {
		lease, err := jobStore.ClaimMatching(ctx, jobs.TargetMacOS, "expiry-worker", func(candidate jobs.Job) bool {
			return candidate.ID.String() == jobID
		})
		if err != nil {
			t.Fatalf("claim attempt %d: %v", expectedAttempt, err)
		}
		if lease.AttemptCount != expectedAttempt {
			t.Fatalf("attempt count = %d, want %d", lease.AttemptCount, expectedAttempt)
		}
		if err := speechStore.SetRenderGenerating(ctx, library.ULID(renderID)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE job SET lease_expires_at = '2000-01-01T00:00:00.000Z' WHERE id = ?`, jobID); err != nil {
			t.Fatal(err)
		}
		affected, err := jobStore.RecoverExpired(ctx)
		if err != nil {
			t.Fatalf("recover attempt %d: %v", expectedAttempt, err)
		}
		if affected != 1 {
			t.Fatalf("recovered jobs on attempt %d = %d, want 1", expectedAttempt, affected)
		}
		job, err := jobStore.Get(ctx, library.ULID(jobID))
		if err != nil {
			t.Fatal(err)
		}
		if expectedAttempt < 3 {
			if job.State != jobs.StateQueued || job.ErrorCode != jobs.LeaseExpiredErrorCode {
				t.Fatalf("retry job after attempt %d = %+v", expectedAttempt, job)
			}
			if _, err := db.Exec(ctx, `UPDATE job SET available_at = ? WHERE id = ?`, store.NowUTC(), jobID); err != nil {
				t.Fatal(err)
			}
		} else if job.State != jobs.StateFailed || job.ErrorCode != jobs.LeaseExpiredErrorCode {
			t.Fatalf("terminal job = %+v", job)
		}
	}

	if err := db.QueryRow(ctx, `SELECT state, error_code FROM audio_render WHERE id = ?`, renderID).Scan(&status, &errorCode); err != nil {
		t.Fatal(err)
	}
	if status != RenderFailed || errorCode != jobs.LeaseExpiredErrorCode {
		t.Fatalf("terminal render state = %q/%q", status, errorCode)
	}
	if err := db.QueryRow(ctx, `SELECT narration_status, narration_error_code FROM article WHERE id = ?`, articleID).Scan(&status, &errorCode); err != nil {
		t.Fatal(err)
	}
	if status != NarrationFailed || errorCode != jobs.LeaseExpiredErrorCode {
		t.Fatalf("terminal article state = %q/%q", status, errorCode)
	}
	narration, err := speechStore.GetNarration(ctx, library.ULID(articleID))
	if err != nil {
		t.Fatal(err)
	}
	if narration.Status != NarrationFailed || narration.ErrorCode != jobs.LeaseExpiredErrorCode || narration.ReadyCount != 0 {
		t.Fatalf("terminal narration = %+v", narration)
	}
}

func TestClearNarrationCountsBindingsAndPreservesSharedThenPurges(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articleOne := "01J00000000000000000000030"
	articleTwo := "01J00000000000000000000040"
	seedSpeechArticle(t, db, articleOne, "01J00000000000000000000031", "01J00000000000000000000032", "01J00000000000000000000033", "01J00000000000000000000034", "bank")
	seedSpeechArticle(t, db, articleTwo, "01J00000000000000000000041", "01J00000000000000000000042", "01J00000000000000000000043", "01J00000000000000000000044", "bank")
	s := NewStore(db)
	if err := s.QueueArticleAudio(ctx, library.ULID(articleOne), false); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueArticleAudio(ctx, library.ULID(articleTwo), false); err != nil {
		t.Fatal(err)
	}
	shared, err := s.ClearNarration(ctx, library.ULID(articleOne))
	if err != nil {
		t.Fatal(err)
	}
	if shared.SentenceCount != 1 || shared.PurgedRenderCount != 0 || shared.ReclaimedBytes != 0 {
		t.Fatalf("shared clear = %+v", shared)
	}
	var remaining int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_sentence_audio WHERE article_id = ?`, articleTwo).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("second article binding count = %d", remaining)
	}

	purged, err := s.ClearNarration(ctx, library.ULID(articleTwo))
	if err != nil {
		t.Fatal(err)
	}
	if purged.SentenceCount != 1 || purged.PurgedRenderCount != 1 {
		t.Fatalf("purge clear = %+v", purged)
	}
	var state, digest string
	if err := db.QueryRow(ctx, `SELECT state, COALESCE((SELECT blob_digest FROM audio_blob_reference WHERE audio_render_id = audio_render.id), '') FROM audio_render WHERE retention_class = 'article_narration'`).Scan(&state, &digest); err != nil {
		t.Fatal(err)
	}
	if state != RenderPurged || digest != "" {
		t.Fatalf("purged render = state %q digest %q", state, digest)
	}

	if err := s.QueueArticleAudio(ctx, library.ULID(articleTwo), true); err != nil {
		t.Fatal(err)
	}
	var regeneratedState string
	if err := db.QueryRow(ctx, `SELECT state FROM audio_render WHERE retention_class = 'article_narration'`).Scan(&regeneratedState); err != nil {
		t.Fatal(err)
	}
	if regeneratedState != RenderQueued {
		t.Fatalf("regenerated render state = %q", regeneratedState)
	}
	var queuedJobs int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE job_type = ? AND state = 'queued'`, jobs.ChatterboxJobType).Scan(&queuedJobs); err != nil {
		t.Fatal(err)
	}
	if queuedJobs != 1 {
		t.Fatalf("queued narration jobs after regeneration = %d", queuedJobs)
	}
}

func TestValidateArtifactRequiresM4AAndBounds(t *testing.T) {
	data := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}
	metadata := ArtifactMetadata{RequestHash: "request", SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SizeBytes: int64(len(data)), MIMEType: AudioMIME, Codec: AudioCodec, SampleRateHz: AudioSampleRate, Channels: AudioChannels, DurationMS: 100}
	if err := ValidateArtifact(metadata, int64(len(data)), data, "request", UnitWord); err != nil {
		t.Fatal(err)
	}
	tooLong := metadata
	tooLong.DurationMS = Limits(UnitWord).MaxDurationMS + 1
	if err := ValidateArtifact(tooLong, int64(len(data)), data, "request", UnitWord); err == nil {
		t.Fatal("overlong word audio unexpectedly accepted")
	}
	bad := metadata
	bad.MIMEType = "audio/mpeg"
	if err := ValidateArtifact(bad, int64(len(data)), data, "request", UnitWord); err == nil {
		t.Fatal("wrong MIME unexpectedly accepted")
	}
}

func TestEnsureUnitAndRenderIdentityIsStable(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var first, second *Unit
	var profile Profile
	if err := db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		first, err = EnsureUnitTx(ctx, tx, UnitInput{Language: "nl", UnitKind: UnitWord, SpokenText: "  Café  "})
		if err != nil {
			return err
		}
		second, err = EnsureUnitTx(ctx, tx, UnitInput{Language: "nl", UnitKind: UnitWord, SpokenText: "Café"})
		if err != nil {
			return err
		}
		var av *Profile
		av, _, err = DefaultProfilesTx(ctx, tx, "nl")
		if err == nil {
			profile = *av
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.NormalizedTextHash != second.NormalizedTextHash {
		t.Fatalf("unit identity = %+v / %+v", first, second)
	}
	var firstRender, secondRender *Render
	if err := db.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		firstRender, err = EnsureRenderTx(ctx, tx, *first, profile, RetentionLexical, false)
		if err != nil {
			return err
		}
		secondRender, err = EnsureRenderTx(ctx, tx, *second, profile, RetentionLexical, false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if firstRender.ID != secondRender.ID || firstRender.RequestHash != secondRender.RequestHash {
		t.Fatalf("render identity = %+v / %+v", firstRender, secondRender)
	}
}
