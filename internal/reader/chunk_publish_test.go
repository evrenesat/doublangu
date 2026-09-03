package reader

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"doublangu/internal/jobs"
	"doublangu/internal/library"
	"doublangu/internal/semantics"
	"doublangu/internal/store"
)

// chunkFixture returns a prepared two-paragraph article and its chunk
// responses, one per block, with a discontinuous construction on block 0
// whose members are only the fixed lexical items.
func chunkFixture(t *testing.T, db *store.DB) (library.ULID, semantics.PreparedArticle, semantics.PreparedChunk, semantics.ValidatedResponse) {
	t.Helper()
	ctx := context.Background()
	article, err := NewArticle("Alinea", "Hij gooit het bijltje erbij neer.\n\nDe bank staat.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	articles := NewStore(db)
	if err := articles.CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	prepared, err := articles.PrepareAnalysis(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Blocks) != 2 {
		t.Fatalf("prepared blocks = %d", len(prepared.Blocks))
	}
	chunk, err := semantics.PrepareChunk(prepared, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := semantics.Response{
		Version: semantics.AnalysisContractVersion,
		NewSenses: []semantics.NewSense{{
			Ref: "gooi-ref", Kind: semantics.KindExpression, CanonicalForm: "bijltje erbij neergooien",
			NormalizedForm: "bijltje erbij neergooien", SenseDiscriminator: "resign",
			PrimaryTranslation: "give up", Alternatives: []string{"throw in the towel"},
		}},
		Constructions: []semantics.Construction{{
			Kind: semantics.KindExpression, Role: "discontinuous_construction",
			NewSenseRef: "gooi-ref", ShadowText: "give up", ConfidenceMilli: 900,
			TokenIDs: []string{"b0:t1", "b0:t3", "b0:t5"},
			Spans: []semantics.SpanRef{
				{BlockIndex: 0, SourceText: "gooit", Occurrence: 0},
				{BlockIndex: 0, SourceText: "bijltje", Occurrence: 0},
				{BlockIndex: 0, SourceText: "neer", Occurrence: 0},
			},
		}},
	}
	for _, token := range chunk.Tokens {
		response.Tokens = append(response.Tokens, semantics.TokenResult{
			TokenID: token.ID, Classification: "unchanged", Kind: semantics.KindWord, ConfidenceMilli: 1000,
		})
	}
	namespaced, err := semantics.NamespaceChunkResponse(0, response, nil)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := semantics.ValidateChunkResponse(chunk, namespaced)
	if err != nil {
		t.Fatal(err)
	}
	return article.ID, prepared, chunk, validated
}

func activeAnalysisJobID(t *testing.T, db *store.DB, articleID library.ULID) library.ULID {
	t.Helper()
	ctx := context.Background()
	var raw string
	if err := db.QueryRow(ctx, `SELECT id FROM job WHERE owner_type = 'article' AND owner_id = ? AND job_type = ? AND state IN ('queued', 'leased', 'running') ORDER BY created_at DESC, id DESC LIMIT 1`, articleID.String(), jobs.AnalysisJobType).Scan(&raw); err != nil {
		t.Fatalf("active job: %v", err)
	}
	return library.ULID(raw)
}

// TestPersistAnalysisChunkIsBlockScoped proves the acceptance rule that
// publishing block 0 cannot change block 1 rows and that only the published
// block receives members, lifecycle state, and lexical jobs.
func TestPersistAnalysisChunkIsBlockScoped(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articleID, prepared, _, validated := chunkFixture(t, db)
	articles := NewStore(db)
	jobID := activeAnalysisJobID(t, db, articleID)
	runID := library.NewULID()
	if err := articles.MarkAnalysisProcessing(ctx, articleID, jobID); err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkBlockProcessing(ctx, articleID, 0, jobID); err != nil {
		t.Fatal(err)
	}
	if err := articles.PersistAnalysisChunk(ctx, articleID, 0, jobID, runID, prepared, validated, semantics.ProviderID, "test-model", "medium"); err != nil {
		t.Fatal(err)
	}
	loaded, err := articles.GetArticle(ctx, articleID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisProgress.TotalParagraphs != 2 || loaded.AnalysisProgress.CompletedParagraphs != 1 {
		t.Fatalf("progress after one chunk = %+v", loaded.AnalysisProgress)
	}
	if loaded.AnalysisProgress.CurrentBlockIndex != 1 || loaded.AnalysisProgress.FailedBlockIndex != -1 {
		t.Fatalf("current/failed after one chunk = %+v", loaded.AnalysisProgress)
	}
	block0 := loaded.Blocks[0]
	block1 := loaded.Blocks[1]
	if block0.AnalysisStatus != BlockReady || !block0.HasAnalysis || !block0.AnalysisIsCurrent {
		t.Fatalf("block 0 state = %+v", block0)
	}
	if block0.PublishedRevision != semantics.AnalysisContractVersion || block0.PublishedModel != "test-model" {
		t.Fatalf("block 0 provenance = %+v", block0)
	}
	if block1.AnalysisStatus != BlockPending || block1.HasAnalysis || block1.AnalysisIsCurrent || len(block1.Occurrences) != 0 {
		t.Fatalf("block 1 must stay untouched: %+v", block1)
	}
	block0Tokens := 0
	for _, token := range prepared.Tokens {
		if token.BlockIndex == 0 {
			block0Tokens++
		}
	}
	if len(block0.Occurrences) != block0Tokens+1 {
		t.Fatalf("block 0 occurrences = %d, want %d tokens + 1 construction", len(block0.Occurrences), block0Tokens)
	}
	// Exact membership: members are only the fixed lexical tokens.
	var constructionID string
	spansByOccurrence := map[string][]string{}
	for index := range block0.Occurrences {
		occurrence := &block0.Occurrences[index]
		var spanTexts []string
		for _, span := range occurrence.Spans {
			spanTexts = append(spanTexts, span.SourceText)
		}
		spansByOccurrence[occurrence.ID.String()] = spanTexts
		if occurrence.Role == OccurrenceDiscontinuousConstruction {
			constructionID = occurrence.ID.String()
			if !occurrence.ShowShadow || occurrence.SubtitleSuppressionReason != SubtitleNone || occurrence.ShadowText != "give up" {
				t.Fatalf("construction occurrence display = %+v", occurrence)
			}
		}
	}
	if constructionID == "" {
		t.Fatal("construction occurrence missing")
	}
	var memberRows []struct {
		token string
		index int
	}
	rows, err := db.Query(ctx, `SELECT token_occurrence_id, member_index FROM article_construction_member WHERE construction_occurrence_id = ? ORDER BY member_index`, constructionID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var token, index string
		if err := rows.Scan(&token, &index); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var memberIndex int
		fmt.Sscanf(index, "%d", &memberIndex)
		memberRows = append(memberRows, struct {
			token string
			index int
		}{token: token, index: memberIndex})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(memberRows) != 3 {
		t.Fatalf("member rows = %d, want 3", len(memberRows))
	}
	for index, row := range memberRows {
		if row.index != index {
			t.Fatalf("member order = %+v", memberRows)
		}
		_ = row.token
	}
	// Derived spans must be the maximal adjacent member runs, not any broad
	// provider span: gooit, bijltje, neer each form their own run here.
	spans, ok := spansByOccurrence[constructionID]
	if !ok || len(spans) != 3 || spans[0] != "gooit" || spans[1] != "bijltje" || spans[2] != "neer" {
		t.Fatalf("construction spans = %v", spans)
	}
	// A second publish of block 0 under a fresh job replaces rows without
	// growing them and keeps the older materialization readable until then.
	secondJob, err := articles.QueueAnalysis(ctx, articleID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkAnalysisProcessing(ctx, articleID, secondJob.ID); err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkBlockProcessing(ctx, articleID, 0, secondJob.ID); err != nil {
		t.Fatal(err)
	}
	if err := articles.PersistAnalysisChunk(ctx, articleID, 0, secondJob.ID, library.NewULID(), prepared, validated, semantics.ProviderID, "test-model", "medium"); err != nil {
		t.Fatal(err)
	}
	var occurrences int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_occurrence WHERE article_block_id = (SELECT id FROM article_block WHERE article_id = ? AND block_index = 0)`, articleID.String()).Scan(&occurrences); err != nil {
		t.Fatal(err)
	}
	if occurrences != block0Tokens+1 {
		t.Errorf("block 0 occurrences after republish = %d", occurrences)
	}
	var membersAfter int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_construction_member`).Scan(&membersAfter); err != nil {
		t.Fatal(err)
	}
	if membersAfter != 3 {
		t.Errorf("member rows after republish = %d, want 3", membersAfter)
	}
}

// TestPersistAnalysisChunkRejectsSupersededJob proves that a superseded
// runner cannot publish a late paragraph: the article job id changed after a
// forced reanalysis, so the old job's publish changes no rows.
func TestPersistAnalysisChunkRejectsSupersededJob(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articleID, prepared, _, validated := chunkFixture(t, db)
	articles := NewStore(db)
	firstJobID := activeAnalysisJobID(t, db, articleID)
	if err := articles.MarkAnalysisProcessing(ctx, articleID, firstJobID); err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkBlockProcessing(ctx, articleID, 0, firstJobID); err != nil {
		t.Fatal(err)
	}
	if err := articles.PersistAnalysisChunk(ctx, articleID, 0, firstJobID, library.NewULID(), prepared, validated, semantics.ProviderID, "test-model", "medium"); err != nil {
		t.Fatal(err)
	}
	// An owner-requested fresh run supersedes the first job and resets every
	// block to pending while preserving the published materialization.
	secondJob, err := articles.QueueAnalysis(ctx, articleID, true, true)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := articles.GetArticle(ctx, articleID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AnalysisProgress.CompletedParagraphs != 0 || loaded.AnalysisProgress.CurrentBlockIndex != 0 {
		t.Fatalf("progress after requeue = %+v", loaded.AnalysisProgress)
	}
	block0 := loaded.Blocks[0]
	if block0.AnalysisStatus != BlockPending || !block0.HasAnalysis || block0.AnalysisIsCurrent {
		t.Fatalf("block 0 after requeue = %+v", block0)
	}
	// The superseded job must fail to publish.
	err = articles.PersistAnalysisChunk(ctx, articleID, 0, firstJobID, library.NewULID(), prepared, validated, semantics.ProviderID, "test-model", "medium")
	if err == nil {
		t.Fatal("superseded publish unexpectedly succeeded")
	}
	var readerErr *Error
	if !errors.As(err, &readerErr) || readerErr.Kind != KindConflict {
		t.Fatalf("superseded publish error = %v", err)
	}
	if secondJob == nil {
		t.Fatal("no second job returned")
	}
	var status string
	if err := db.QueryRow(ctx, `SELECT analysis_status FROM article_block WHERE article_id = ? AND block_index = 0`, articleID.String()).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(BlockPending) {
		t.Errorf("block 0 status after rejected publish = %q, want pending", status)
	}
}

// TestUnchangedTokensSuppressAndPronunciationsFollowBlocks proves the display
// invariant for effective subtitles and that lexical jobs exist only for the
// published paragraph.
func TestUnchangedTokensSuppressAndPronunciationsFollowBlocks(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articleID, _, _, _ := chunkFixture(t, db)
	articles := NewStore(db)
	jobID := activeAnalysisJobID(t, db, articleID)
	// Nothing is published yet: no lexical render must exist.
	var rendersBefore int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audio_render WHERE retention_class = 'lexical_permanent'`).Scan(&rendersBefore); err != nil {
		t.Fatal(err)
	}
	if rendersBefore != 0 {
		t.Fatalf("lexical renders before publish = %d", rendersBefore)
	}
	// Publish only block 1 (a plain paragraph without constructions).
	prepared, err := articles.PrepareAnalysis(ctx, articleID)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := semantics.PrepareChunk(prepared, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := semantics.Response{
		Version: semantics.AnalysisContractVersion,
		NewSenses: []semantics.NewSense{{
			Ref: "sofa-ref", Kind: semantics.KindWord, CanonicalForm: "bank", NormalizedForm: "bank",
			SenseDiscriminator: "sofa", PrimaryTranslation: "sofa",
		}},
	}
	for _, token := range chunk.Tokens {
		result := semantics.TokenResult{TokenID: token.ID, Classification: "unchanged", Kind: semantics.KindWord, ConfidenceMilli: 1000}
		if token.NormalizedForm == "bank" {
			result.Classification = "lexical"
			result.NewSenseRef = "sofa-ref"
			result.ShadowText = "sofa"
		}
		response.Tokens = append(response.Tokens, result)
	}
	namespaced, err := semantics.NamespaceChunkResponse(1, response, nil)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := semantics.ValidateChunkResponse(chunk, namespaced)
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkAnalysisProcessing(ctx, articleID, jobID); err != nil {
		t.Fatal(err)
	}
	if err := articles.MarkBlockProcessing(ctx, articleID, 1, jobID); err != nil {
		t.Fatal(err)
	}
	if err := articles.PersistAnalysisChunk(ctx, articleID, 1, jobID, library.NewULID(), prepared, validated, semantics.ProviderID, "test-model", "medium"); err != nil {
		t.Fatal(err)
	}
	loaded, err := articles.GetArticle(ctx, articleID)
	if err != nil {
		t.Fatal(err)
	}
	for _, occurrence := range loaded.Blocks[1].Occurrences {
		if occurrence.ShadowText == "sofa" {
			if !occurrence.ShowShadow || occurrence.SubtitleSuppressionReason != SubtitleNone {
				t.Errorf("translated token display = %+v", occurrence)
			}
		} else {
			// Unchanged function words have no effective subtitle.
			if occurrence.ShowShadow || occurrence.SubtitleSuppressionReason != SubtitleSpecialToken {
				t.Errorf("unchanged token display = %+v", occurrence)
			}
		}
	}
	var lexicalRenders int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audio_render WHERE retention_class = 'lexical_permanent'`).Scan(&lexicalRenders); err != nil {
		t.Fatal(err)
	}
	if lexicalRenders == 0 {
		t.Fatal("no lexical render for the published block")
	}
	var block0Bindings int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM article_occurrence_audio WHERE article_occurrence_id IN (SELECT id FROM article_occurrence WHERE article_block_id = (SELECT id FROM article_block WHERE article_id = ? AND block_index = 0))`, articleID.String()).Scan(&block0Bindings); err != nil {
		t.Fatal(err)
	}
	if block0Bindings != 0 {
		t.Errorf("unpublished block 0 has %d lexical bindings", block0Bindings)
	}
}

// TestMarkAnalysisProcessingIsJobScoped proves that a stale job superseded by
// a newer queue cannot mark the article processing: the transition requires
// the article to still belong to the caller's job.
func TestMarkAnalysisProcessingIsJobScoped(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	article, err := NewArticle("Scope", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	articles := NewStore(db)
	if err := articles.CreateArticleQueued(ctx, &article); err != nil {
		t.Fatal(err)
	}
	activeJob := activeAnalysisJobID(t, db, article.ID)
	staleJob := library.NewULID()
	err = articles.MarkAnalysisProcessing(ctx, article.ID, staleJob)
	if err == nil {
		t.Fatal("stale job unexpectedly marked the article processing")
	}
	var readerErr *Error
	if !errors.As(err, &readerErr) || readerErr.Kind != KindConflict {
		t.Fatalf("stale job error = %v", err)
	}
	var status string
	if err := db.QueryRow(ctx, `SELECT analysis_status FROM article WHERE id = ?`, article.ID.String()).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(AnalysisQueued) {
		t.Fatalf("article status after stale transition = %q, want queued", status)
	}
	if err := articles.MarkAnalysisProcessing(ctx, article.ID, activeJob); err != nil {
		t.Fatalf("active job transition failed: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT analysis_status FROM article WHERE id = ?`, article.ID.String()).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(AnalysisProcessing) {
		t.Fatalf("article status after active transition = %q", status)
	}
}

// TestArticleProfileSnapshotPersistence proves the resolved profile snapshot
// is stored on the article at creation and stays blank for legacy creation.
func TestArticleProfileSnapshotPersistence(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles := NewStore(db)

	snapshot := profileSnapshotFixture(t)
	article, err := NewArticle("Snap", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &article, snapshot); err != nil {
		t.Fatal(err)
	}
	var profileID, profileName, snapshotHash string
	if err := db.QueryRow(ctx, `SELECT analysis_profile_id, analysis_profile_name, analysis_pipeline_snapshot_hash FROM article WHERE id = ?`, article.ID.String()).Scan(&profileID, &profileName, &snapshotHash); err != nil {
		t.Fatal(err)
	}
	if profileID != snapshot.ID || profileName != snapshot.Name || snapshotHash == "" {
		t.Fatalf("snapshot columns = %q/%q/%q", profileID, profileName, snapshotHash)
	}
	// Legacy creation path leaves the columns blank.
	legacy, err := NewArticle("Legacy", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueued(ctx, &legacy); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT analysis_profile_id, analysis_pipeline_snapshot_hash FROM article WHERE id = ?`, legacy.ID.String()).Scan(&profileID, &snapshotHash); err != nil {
		t.Fatal(err)
	}
	if profileID != "" || snapshotHash != "" {
		t.Fatalf("legacy snapshot columns = %q/%q", profileID, snapshotHash)
	}
}

// TestQueueAnalysisWithProfileSnapshotSemantics proves the immutable snapshot
// rules: creation stores profile A; a fresh run with profile B replaces it;
// a normal retry with a stale caller profile still reuses the stored B.
func TestQueueAnalysisWithProfileSnapshotSemantics(t *testing.T) {
	db, err := store.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	articles := NewStore(db)
	profileA := profileSnapshotFixture(t)
	profileB := *profileA
	profileB.ID = "profile-2"
	profileB.Name = "Profile B"
	hashA, err := profileA.SnapshotHash()
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := profileB.SnapshotHash()
	if err != nil {
		t.Fatal(err)
	}
	article, err := NewArticle("SnapQ", "Een zin.", "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := articles.CreateArticleQueuedWithProfile(ctx, &article, profileA); err != nil {
		t.Fatal(err)
	}
	var storedHash string
	if err := db.QueryRow(ctx, `SELECT analysis_pipeline_snapshot_hash FROM article WHERE id = ?`, article.ID.String()).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != hashA {
		t.Fatalf("creation snapshot = %q, want %q", storedHash, hashA)
	}
	// A fresh run with profile B supersedes and persists B.
	if _, err := articles.QueueAnalysisWithProfile(ctx, article.ID, true, true, &profileB); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT analysis_pipeline_snapshot_hash FROM article WHERE id = ?`, article.ID.String()).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != hashB {
		t.Fatalf("fresh snapshot = %q, want %q", storedHash, hashB)
	}
	// A normal retry ignores a stale caller profile and keeps the stored B.
	if _, err := articles.QueueAnalysisWithProfile(ctx, article.ID, false, false, profileA); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT analysis_pipeline_snapshot_hash FROM article WHERE id = ?`, article.ID.String()).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != hashB {
		t.Fatalf("normal retry snapshot = %q, want stored %q", storedHash, hashB)
	}
	var queuedJobs int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM job WHERE owner_type = 'article' AND owner_id = ? AND job_type = ? AND state IN ('queued', 'leased', 'running')`, article.ID.String(), jobs.AnalysisJobType).Scan(&queuedJobs); err != nil {
		t.Fatal(err)
	}
	if queuedJobs != 1 {
		t.Fatalf("active jobs = %d", queuedJobs)
	}
	// A fresh run without any snapshot is rejected before state changes.
	if _, err := articles.QueueAnalysisWithProfile(ctx, article.ID, true, true, nil); err == nil {
		t.Fatal("fresh run without a profile accepted")
	}
}
