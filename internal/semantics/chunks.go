package semantics

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// PreparedChunk is the complete deterministic input for one isolated
// paragraph turn. Block, token, and sentence identities remain those of the
// complete article; they are never retokenized or renumbered for a chunk.
type PreparedChunk struct {
	Title                string
	SourceLanguage       string
	TargetLanguage       string
	ContentHash          string
	Block                Block
	Tokens               []Token
	Candidates           []SenseCandidate
	PriorValidatedSenses []NewSense
	Sentences            []ResolvedSentence
	InputHash            string
}

// ChunkResult associates an un-namespaced provider response with the exact
// prepared chunk it was produced for. MergeChunks verifies both before it
// permits the response to become part of an article response.
type ChunkResult struct {
	Chunk    PreparedChunk
	Response Response
}

// PreparedInputHash returns the deterministic identity of the complete
// prepared provider input, including the ordered candidate values. It is
// intentionally separate from ContentHash: changing the local lexicon must
// invalidate a semantic cache even when article source bytes are unchanged.
func PreparedInputHash(input PreparedArticle) string {
	var b bytes.Buffer
	writeHashString(&b, "doublangu.prepared-input.v1")
	writeHashString(&b, input.Title)
	writeHashString(&b, input.SourceLanguage)
	writeHashString(&b, input.TargetLanguage)
	writeHashString(&b, input.ContentHash)
	for _, block := range input.Blocks {
		writeHashInt(&b, block.BlockIndex)
		writeHashString(&b, block.SourceText)
	}
	for _, token := range input.Tokens {
		writeHashString(&b, token.ID)
		writeHashInt(&b, token.BlockIndex)
		writeHashInt(&b, token.TokenIndex)
		writeHashInt(&b, token.StartUTF16)
		writeHashInt(&b, token.EndUTF16)
		writeHashString(&b, token.SourceText)
		writeHashString(&b, token.NormalizedForm)
		writeHashString(&b, token.Lemma)
	}
	for _, sentence := range input.Sentences {
		writeResolvedSentenceHash(&b, sentence)
	}
	for _, candidate := range input.Candidates {
		writeHashString(&b, candidate.ID)
		writeHashString(&b, candidate.SemanticItemID)
		writeHashString(&b, candidate.SourceLanguage)
		writeHashString(&b, candidate.TargetLanguage)
		writeHashString(&b, string(candidate.Kind))
		writeHashString(&b, candidate.CanonicalForm)
		writeHashString(&b, candidate.NormalizedForm)
		writeHashString(&b, candidate.Lemma)
		writeHashString(&b, candidate.PartOfSpeech)
		writeHashString(&b, candidate.PrimaryTranslation)
		writeHashString(&b, candidate.SenseDiscriminator)
	}
	return hashBuffer(b.Bytes())
}

// BlockHash returns the identity of one exact source block.
func BlockHash(block Block) string {
	var b bytes.Buffer
	writeHashString(&b, "doublangu.analysis-block.v1")
	writeHashInt(&b, block.BlockIndex)
	writeHashString(&b, block.SourceText)
	return hashBuffer(b.Bytes())
}

// CarryHash returns the identity of the ordered validated-sense carry pool.
func CarryHash(senses []NewSense) string {
	var b bytes.Buffer
	writeHashString(&b, "doublangu.analysis-carry.v1")
	for _, sense := range senses {
		writeSenseHash(&b, sense)
	}
	return hashBuffer(b.Bytes())
}

// PrepareChunk derives one paragraph input and filters candidates and prior
// senses with the same deterministic relevance rule.
func PrepareChunk(input PreparedArticle, blockIndex int, prior []NewSense) (PreparedChunk, error) {
	if blockIndex < 0 || blockIndex >= len(input.Blocks) {
		return PreparedChunk{}, fmt.Errorf("invalid chunk block index %d", blockIndex)
	}
	block := input.Blocks[blockIndex]
	chunk := PreparedChunk{
		Title:          input.Title,
		SourceLanguage: input.SourceLanguage,
		TargetLanguage: input.TargetLanguage,
		ContentHash:    input.ContentHash,
		Block:          block,
	}
	for _, token := range input.Tokens {
		if token.BlockIndex == blockIndex {
			chunk.Tokens = append(chunk.Tokens, token)
		}
	}
	for _, sentence := range input.Sentences {
		if sentence.Span.BlockIndex == blockIndex {
			chunk.Sentences = append(chunk.Sentences, sentence)
		}
	}
	paragraphText := normalizedForRelevance(block.SourceText)
	for _, candidate := range input.Candidates {
		if relevantToTokens(candidate.NormalizedForm, candidate.Lemma, chunk.Tokens) || relevantCanonical(candidate.CanonicalForm, paragraphText) {
			chunk.Candidates = append(chunk.Candidates, candidate)
		}
	}
	for _, sense := range prior {
		if relevantToTokens(sense.NormalizedForm, sense.Lemma, chunk.Tokens) || relevantCanonical(sense.CanonicalForm, paragraphText) {
			chunk.PriorValidatedSenses = append(chunk.PriorValidatedSenses, sense)
		}
	}
	chunk.InputHash = chunkInputHash(chunk)
	return chunk, nil
}

// PrepareChunks derives all chunks without carry-forward values. It is useful
// for inspection and tests; the runner derives each later chunk again after
// the preceding response has been validated.
func PrepareChunks(input PreparedArticle) ([]PreparedChunk, error) {
	chunks := make([]PreparedChunk, 0, len(input.Blocks))
	for index := range input.Blocks {
		chunk, err := PrepareChunk(input, index, nil)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

func chunkInputHash(chunk PreparedChunk) string {
	var b bytes.Buffer
	writeHashString(&b, "doublangu.chunk-input.v1")
	writeHashString(&b, chunk.Title)
	writeHashString(&b, chunk.SourceLanguage)
	writeHashString(&b, chunk.TargetLanguage)
	writeHashString(&b, chunk.ContentHash)
	writeHashInt(&b, chunk.Block.BlockIndex)
	writeHashString(&b, chunk.Block.SourceText)
	for _, token := range chunk.Tokens {
		writeHashString(&b, token.ID)
		writeHashInt(&b, token.BlockIndex)
		writeHashInt(&b, token.TokenIndex)
		writeHashInt(&b, token.StartUTF16)
		writeHashInt(&b, token.EndUTF16)
		writeHashString(&b, token.SourceText)
		writeHashString(&b, token.NormalizedForm)
		writeHashString(&b, token.Lemma)
	}
	for _, sentence := range chunk.Sentences {
		writeResolvedSentenceHash(&b, sentence)
	}
	for _, candidate := range chunk.Candidates {
		writeCandidateHash(&b, candidate)
	}
	for _, sense := range chunk.PriorValidatedSenses {
		writeSenseHash(&b, sense)
	}
	return hashBuffer(b.Bytes())
}

func writeCandidateHash(b *bytes.Buffer, candidate SenseCandidate) {
	writeHashString(b, candidate.ID)
	writeHashString(b, candidate.SemanticItemID)
	writeHashString(b, candidate.SourceLanguage)
	writeHashString(b, candidate.TargetLanguage)
	writeHashString(b, string(candidate.Kind))
	writeHashString(b, candidate.CanonicalForm)
	writeHashString(b, candidate.NormalizedForm)
	writeHashString(b, candidate.Lemma)
	writeHashString(b, candidate.PartOfSpeech)
	writeHashString(b, candidate.PrimaryTranslation)
	writeHashString(b, candidate.SenseDiscriminator)
}

// writeResolvedSentenceHash contributes one stable sentence anchor to a hash.
// The source-owned anchors are part of the deterministic input identity, so a
// segmentation or sentence change invalidates prepared-input and chunk caches.
func writeResolvedSentenceHash(b *bytes.Buffer, sentence ResolvedSentence) {
	writeHashInt(b, sentence.Span.BlockIndex)
	writeHashInt(b, sentence.Index)
	writeHashInt(b, sentence.Span.StartUTF16)
	writeHashInt(b, sentence.Span.EndUTF16)
	writeHashString(b, sentence.Span.SourceText)
}

func writeSenseHash(b *bytes.Buffer, sense NewSense) {
	writeHashString(b, sense.Ref)
	writeHashString(b, string(sense.Kind))
	writeHashString(b, sense.CanonicalForm)
	writeHashString(b, sense.NormalizedForm)
	writeHashString(b, sense.Lemma)
	writeHashString(b, sense.PartOfSpeech)
	writeHashString(b, sense.SenseDiscriminator)
	writeHashString(b, sense.PrimaryTranslation)
	for _, alternative := range sense.Alternatives {
		writeHashString(b, alternative)
	}
	writeHashString(b, sense.LiteralTranslation)
	writeHashString(b, sense.MeaningNote)
	writeHashString(b, sense.UsageNote)
	writeHashString(b, sense.PartsNote)
	writeHashString(b, sense.CanonicalPronunciationText)
}

func writeHashString(b *bytes.Buffer, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	b.Write(length[:])
	b.WriteString(value)
}

func writeHashInt(b *bytes.Buffer, value int) {
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(value))
	b.Write(number[:])
}

func hashBuffer(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func normalizedForRelevance(value string) string {
	normalized, err := NormalizeForm(value)
	if err != nil {
		return ""
	}
	return normalized
}

func relevantToTokens(normalizedForm, lemma string, tokens []Token) bool {
	for _, token := range tokens {
		for _, candidateKey := range []string{normalizedForm, lemma} {
			if candidateKey == "" {
				continue
			}
			if candidateKey == token.NormalizedForm || candidateKey == token.Lemma {
				return true
			}
		}
	}
	return false
}

func relevantCanonical(canonical, paragraphText string) bool {
	normalized := normalizedForRelevance(canonical)
	return normalized != "" && paragraphText != "" && strings.Contains(paragraphText, normalized)
}

// ValidateChunkResponse applies the normal semantic validator while allowing
// prior validated senses and then enforces the chunk's single-block boundary.
func ValidateChunkResponse(chunk PreparedChunk, response Response) (ValidatedResponse, error) {
	if chunk.Block.BlockIndex < 0 {
		return ValidatedResponse{}, errors.New("chunk has an invalid block index")
	}
	if chunk.InputHash != "" && chunk.InputHash != chunkInputHash(chunk) {
		return ValidatedResponse{}, errors.New("chunk input hash does not match its fields")
	}
	blocks := make([]Block, chunk.Block.BlockIndex+1)
	blocks[chunk.Block.BlockIndex] = chunk.Block
	input := PreparedArticle{
		Title:          chunk.Title,
		SourceLanguage: chunk.SourceLanguage,
		TargetLanguage: chunk.TargetLanguage,
		ContentHash:    chunk.ContentHash,
		Blocks:         blocks,
		Tokens:         append([]Token(nil), chunk.Tokens...),
		Candidates:     append([]SenseCandidate(nil), chunk.Candidates...),
		Sentences:      append([]ResolvedSentence(nil), chunk.Sentences...),
	}
	validated, err := ValidateResponseWithPrior(input, response, chunk.PriorValidatedSenses)
	if err != nil {
		return ValidatedResponse{}, err
	}
	for index, sentence := range validated.Sentences {
		if sentence.Span.BlockIndex != chunk.Block.BlockIndex {
			return ValidatedResponse{}, fmt.Errorf("sentences[%d] crosses the chunk block boundary", index)
		}
	}
	for index, construction := range validated.Constructions {
		for spanIndex, span := range construction.Spans {
			if span.BlockIndex != chunk.Block.BlockIndex {
				return ValidatedResponse{}, fmt.Errorf("construction %d span %d crosses the chunk block boundary", index, spanIndex)
			}
		}
		for tokenIndex, tokenID := range construction.Construction.TokenIDs {
			for _, token := range chunk.Tokens {
				if token.ID == tokenID && token.BlockIndex != chunk.Block.BlockIndex {
					return ValidatedResponse{}, fmt.Errorf("construction %d token %d crosses the chunk block boundary", index, tokenIndex)
				}
			}
		}
	}
	for index, sense := range response.NewSenses {
		if utf8.RuneCountInString(sense.Ref) > 96 {
			return ValidatedResponse{}, fmt.Errorf("new_senses[%d].ref is longer than 96 Unicode scalar values", index)
		}
	}
	return validated, nil
}

// NamespaceChunkResponse rewrites only local new-sense references. References
// to prior validated senses are already namespaced and remain unchanged.
func NamespaceChunkResponse(blockIndex int, response Response, prior []NewSense) (Response, error) {
	priorRefs := make(map[string]struct{}, len(prior))
	for _, sense := range prior {
		if sense.Ref == "" {
			return Response{}, errors.New("prior sense has an empty ref")
		}
		priorRefs[sense.Ref] = struct{}{}
	}
	rewrite := make(map[string]string, len(response.NewSenses))
	result := response
	result.NewSenses = append([]NewSense(nil), response.NewSenses...)
	result.Tokens = append([]TokenResult(nil), response.Tokens...)
	result.Constructions = append([]Construction(nil), response.Constructions...)
	for index := range result.NewSenses {
		local := result.NewSenses[index].Ref
		if _, collides := priorRefs[local]; collides {
			return Response{}, fmt.Errorf("new sense ref %q collides with a prior validated sense", local)
		}
		if local == "" {
			return Response{}, fmt.Errorf("new_senses[%d] has an empty ref", index)
		}
		namespaced := fmt.Sprintf("b%d:%s", blockIndex, local)
		if utf8.RuneCountInString(namespaced) > 120 {
			return Response{}, fmt.Errorf("new sense ref %q exceeds the 120-character namespaced contract limit", local)
		}
		if _, collides := priorRefs[namespaced]; collides {
			return Response{}, fmt.Errorf("namespaced new sense ref %q collides with a prior validated sense", namespaced)
		}
		for existingLocal, existingNamespaced := range rewrite {
			if existingNamespaced == namespaced {
				return Response{}, fmt.Errorf("new sense refs %q and %q produce the same namespaced ref", existingLocal, local)
			}
		}
		rewrite[local] = namespaced
		result.NewSenses[index].Ref = namespaced
	}
	rewriteRef := func(ref string) string {
		if namespaced, ok := rewrite[ref]; ok {
			return namespaced
		}
		return ref
	}
	for index := range result.Tokens {
		result.Tokens[index].NewSenseRef = rewriteRef(result.Tokens[index].NewSenseRef)
	}
	for index := range result.Constructions {
		result.Constructions[index].NewSenseRef = rewriteRef(result.Constructions[index].NewSenseRef)
	}
	return result, nil
}

// MergeChunks validates, namespaces, and merges one response per article
// block. Chunks must be supplied in block order and must carry the exact
// carry-forward context derived from earlier responses.
func MergeChunks(input PreparedArticle, chunks []ChunkResult) (Response, error) {
	if len(chunks) != len(input.Blocks) {
		return Response{}, fmt.Errorf("got %d chunks for %d article blocks", len(chunks), len(input.Blocks))
	}
	merged := Response{Version: AnalysisContractVersion}
	prior := make([]NewSense, 0)
	for index, item := range chunks {
		if item.Chunk.Block.BlockIndex != index {
			return Response{}, fmt.Errorf("chunk %d has block index %d; chunks must be in block order", index, item.Chunk.Block.BlockIndex)
		}
		expected, err := PrepareChunk(input, index, prior)
		if err != nil {
			return Response{}, err
		}
		if item.Chunk.InputHash != expected.InputHash {
			return Response{}, fmt.Errorf("chunk %d input hash does not match current carry-forward context", index)
		}
		validated, err := ValidateChunkResponse(expected, item.Response)
		if err != nil {
			return Response{}, fmt.Errorf("chunk %d: %w", index, err)
		}
		namespaced, err := NamespaceChunkResponse(index, validated.Response, prior)
		if err != nil {
			return Response{}, fmt.Errorf("chunk %d: %w", index, err)
		}
		merged.Tokens = append(merged.Tokens, namespaced.Tokens...)
		merged.NewSenses = append(merged.NewSenses, namespaced.NewSenses...)
		merged.Constructions = append(merged.Constructions, namespaced.Constructions...)
		prior = append(prior, namespaced.NewSenses...)
	}
	if _, err := ValidateResponse(input, merged); err != nil {
		return Response{}, fmt.Errorf("merged response: %w", err)
	}
	return merged, nil
}
