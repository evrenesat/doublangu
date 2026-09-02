package semantics

import (
	"encoding/json"
	"strings"
	"testing"
)

func preparedChunkFixture(t *testing.T) PreparedArticle {
	t.Helper()
	input, err := Prepare("A lesson", "nl", "en", []Block{
		{BlockIndex: 0, SourceText: "De bank."},
		{BlockIndex: 1, SourceText: "De bank."},
	}, []SenseCandidate{
		{ID: "sense-bank", SemanticItemID: "item-bank", SourceLanguage: "nl", TargetLanguage: "en", Kind: KindWord, CanonicalForm: "bank", NormalizedForm: "bank", PrimaryTranslation: "bench", SenseDiscriminator: "furniture"},
		{ID: "sense-house", SemanticItemID: "item-house", SourceLanguage: "nl", TargetLanguage: "en", Kind: KindWord, CanonicalForm: "huis", NormalizedForm: "huis", PrimaryTranslation: "house", SenseDiscriminator: "building"},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return attachWholeBlockAnchors(t, input)
}

func fixtureNewSense(ref, translation string) NewSense {
	return NewSense{
		Ref: ref, Kind: KindWord, CanonicalForm: "bank", NormalizedForm: "bank",
		SenseDiscriminator: "furniture", PrimaryTranslation: translation,
		Alternatives: []string{},
	}
}

func fixtureChunkResponse(chunk PreparedChunk, bankRef string) Response {
	response := Response{
		Version:   AnalysisContractVersion,
		Tokens:    make([]TokenResult, 0, len(chunk.Tokens)),
		NewSenses: []NewSense{}, Constructions: []Construction{},
	}
	for _, token := range chunk.Tokens {
		result := TokenResult{TokenID: token.ID, Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000}
		if strings.EqualFold(token.SourceText, "bank") {
			result.Classification = "known"
			result.NewSenseRef = bankRef
			result.ShadowText = "bench"
		}
		response.Tokens = append(response.Tokens, result)
	}
	return response
}

func TestPrepareChunkFiltersCandidatesAndCarryByDeterministicRelevance(t *testing.T) {
	input := preparedChunkFixture(t)
	house := fixtureNewSense("b0:house", "house")
	house.CanonicalForm, house.NormalizedForm = "huis", "huis"
	prior := []NewSense{fixtureNewSense("b0:bank", "bench"), house}
	chunk, err := PrepareChunk(input, 1, prior)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunk.Tokens) != 2 || chunk.Tokens[0].ID != "b1:t2" || chunk.Tokens[1].ID != "b1:t3" {
		t.Fatalf("chunk tokens = %+v", chunk.Tokens)
	}
	if len(chunk.Candidates) != 1 || chunk.Candidates[0].ID != "sense-bank" {
		t.Fatalf("chunk candidates = %+v", chunk.Candidates)
	}
	if len(chunk.PriorValidatedSenses) != 1 || chunk.PriorValidatedSenses[0].Ref != "b0:bank" {
		t.Fatalf("chunk carry = %+v", chunk.PriorValidatedSenses)
	}
	if chunk.InputHash == "" || chunk.InputHash != chunkInputHash(chunk) {
		t.Fatalf("chunk input hash = %q", chunk.InputHash)
	}

	changed := append([]NewSense(nil), prior...)
	changed[0].PrimaryTranslation = "financial institution"
	changedChunk, err := PrepareChunk(input, 1, changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedChunk.InputHash == chunk.InputHash {
		t.Fatal("changed relevant carry context reused the old chunk hash")
	}
}

func TestChunkValidationRejectsForeignRelationsAndDuplicateRefs(t *testing.T) {
	input := preparedChunkFixture(t)
	chunk, err := PrepareChunk(input, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	foreignToken := fixtureChunkResponse(chunk, "")
	foreignToken.Tokens = append(foreignToken.Tokens, TokenResult{TokenID: "b1:t2", Classification: "unchanged", Kind: KindWord, ConfidenceMilli: 1000})
	if _, err := ValidateChunkResponse(chunk, foreignToken); err == nil {
		t.Fatal("foreign token unexpectedly passed validation")
	}

	duplicate := fixtureChunkResponse(chunk, "local")
	duplicate.NewSenses = []NewSense{fixtureNewSense("local", "bench"), fixtureNewSense("local", "seat")}
	if _, err := ValidateChunkResponse(chunk, duplicate); err == nil {
		t.Fatal("duplicate local references unexpectedly passed validation")
	}
}

func TestMergeChunksNamespacesCarryAndRunsFinalValidation(t *testing.T) {
	input := preparedChunkFixture(t)
	first, err := PrepareChunk(input, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstResponse := fixtureChunkResponse(first, "local-bank")
	firstResponse.NewSenses = []NewSense{fixtureNewSense("local-bank", "bench")}
	firstNamespaced, err := NamespaceChunkResponse(0, firstResponse, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareChunk(input, 1, firstNamespaced.NewSenses)
	if err != nil {
		t.Fatal(err)
	}
	secondResponse := fixtureChunkResponse(second, "b0:local-bank")

	merged, err := MergeChunks(input, []ChunkResult{
		{Chunk: first, Response: firstResponse},
		{Chunk: second, Response: secondResponse},
	})
	if err != nil {
		t.Fatalf("MergeChunks: %v", err)
	}
	if len(merged.NewSenses) != 1 || merged.NewSenses[0].Ref != "b0:local-bank" {
		t.Fatalf("merged senses = %+v", merged.NewSenses)
	}
	if merged.Tokens[3].NewSenseRef != "b0:local-bank" {
		t.Fatalf("carried token ref = %q", merged.Tokens[3].NewSenseRef)
	}
	if _, err := ValidateResponse(input, merged); err != nil {
		t.Fatalf("final validation: %v", err)
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	encodedAgain, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(encodedAgain) {
		t.Fatal("merged JSON is not deterministic")
	}

	if _, err := MergeChunks(input, []ChunkResult{
		{Chunk: second, Response: secondResponse},
		{Chunk: first, Response: firstResponse},
	}); err == nil {
		t.Fatal("out-of-order chunks unexpectedly merged")
	}
}

func TestChunkPunctuationOnlyBlockAndNamespacedCollision(t *testing.T) {
	input, err := Prepare("Punctuation", "nl", "en", []Block{
		{BlockIndex: 0, SourceText: "..."},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := PrepareChunk(input, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunk.Tokens) != 0 {
		t.Fatalf("punctuation chunk has tokens: %+v", chunk.Tokens)
	}
	if _, err := ValidateChunkResponse(chunk, fixtureChunkResponse(chunk, "")); err != nil {
		t.Fatalf("punctuation chunk validation: %v", err)
	}

	response := Response{NewSenses: []NewSense{fixtureNewSense("foo", "bench")}}
	prior := []NewSense{fixtureNewSense("b0:foo", "bench")}
	if _, err := NamespaceChunkResponse(0, response, prior); err == nil {
		t.Fatal("namespaced reference collision unexpectedly passed")
	}
}

func TestPreparedInputHashIncludesCandidatesAndContent(t *testing.T) {
	input := preparedChunkFixture(t)
	changedCandidate := input
	changedCandidate.Candidates = append([]SenseCandidate(nil), input.Candidates...)
	changedCandidate.Candidates[0].PrimaryTranslation = "financial institution"
	if PreparedInputHash(input) == PreparedInputHash(changedCandidate) {
		t.Fatal("candidate change reused prepared input hash")
	}
	changedContent := input
	changedContent.ContentHash = "changed"
	if PreparedInputHash(input) == PreparedInputHash(changedContent) {
		t.Fatal("content hash change reused prepared input hash")
	}
}

func resolvedAnchor(block Block, index int, text string) (ResolvedSentence, error) {
	span, err := ResolveSpan(block, text, 0)
	if err != nil {
		return ResolvedSentence{}, err
	}
	return ResolvedSentence{Index: index, Span: span}, nil
}

func TestPreparedInputHashIncludesSentenceAnchors(t *testing.T) {
	input, err := Prepare("Zinnen", "nl", "en", []Block{
		{BlockIndex: 0, SourceText: "De bank staat. Hij is oud."},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	block := input.Blocks[0]
	// Segmentation A: two conventional sentences.
	segmented := input
	first, err := resolvedAnchor(block, 0, "De bank staat.")
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolvedAnchor(block, 1, "Hij is oud.")
	if err != nil {
		t.Fatal(err)
	}
	segmented.Sentences = []ResolvedSentence{first, second}
	// Segmentation B: the same source with one merged narration unit.
	merged, err := resolvedAnchor(block, 0, "De bank staat. Hij is oud.")
	if err != nil {
		t.Fatal(err)
	}
	mergedInput := input
	mergedInput.Sentences = []ResolvedSentence{merged}
	// Identical inputs hash identically.
	if PreparedInputHash(segmented) != PreparedInputHash(segmented) {
		t.Fatal("identical prepared inputs produced different hashes")
	}
	// A changed sentence segmentation must invalidate the prepared-input
	// identity even though the source bytes are identical.
	if PreparedInputHash(segmented) == PreparedInputHash(mergedInput) {
		t.Fatal("changed sentence anchors reused the prepared input hash")
	}
}

func TestPrepareChunkFiltersSentencesPerBlock(t *testing.T) {
	input, err := Prepare("Blokken", "nl", "en", []Block{
		{BlockIndex: 0, SourceText: "De bank staat. Hij is oud."},
		{BlockIndex: 1, SourceText: "Een huis. Het is groot."},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	perBlockTexts := map[int][]string{
		0: {"De bank staat.", "Hij is oud."},
		1: {"Een huis.", "Het is groot."},
	}
	for _, block := range input.Blocks {
		for sentenceIndex, text := range perBlockTexts[block.BlockIndex] {
			anchor, err := resolvedAnchor(block, sentenceIndex, text)
			if err != nil {
				t.Fatal(err)
			}
			input.Sentences = append(input.Sentences, anchor)
		}
	}
	if len(input.Sentences) != 4 {
		t.Fatalf("input sentences = %d, want 4", len(input.Sentences))
	}
	chunk, err := PrepareChunk(input, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunk.Sentences) != 2 {
		t.Fatalf("chunk sentences = %+v, want the two block-1 anchors", chunk.Sentences)
	}
	for _, sentence := range chunk.Sentences {
		if sentence.Span.BlockIndex != 1 {
			t.Errorf("chunk sentence crosses block boundary: %+v", sentence)
		}
	}
	if chunk.InputHash == "" || chunk.InputHash != chunkInputHash(chunk) {
		t.Fatalf("chunk input hash does not match its fields")
	}
	wholeHash := PreparedInputHash(input)
	if wholeHash == "" || wholeHash == chunk.InputHash {
		t.Fatalf("chunk hash unexpectedly equals the whole prepared input hash")
	}
}
