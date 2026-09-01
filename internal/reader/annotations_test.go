package reader

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeCandidatesPrefersGroupsAndComputesUTF16Offsets(t *testing.T) {
	article := testArticle(t, "😀 Ik wil tot rust komen en tot rust komen.")
	candidates := []Candidate{
		testCandidate("rust", KindWord, 0, 0, "to rest", true),
		testCandidate("tot rust komen", KindExpression, 0, 0, "to calm down", true),
		testCandidate("komen", KindWord, 0, 0, "to come", true),
		testCandidate("tot rust komen", KindExpression, 0, 1, "to settle down", false),
	}
	result, err := NormalizeCandidates(&article, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Annotations) != 2 {
		t.Fatalf("annotations = %d, want 2: %+v", len(result.Annotations), result.Annotations)
	}
	if result.Annotations[0].Kind != KindExpression || result.Annotations[0].SourceText != "tot rust komen" {
		t.Fatalf("first annotation = %+v", result.Annotations[0])
	}
	if result.Annotations[0].StartUTF16 != 10 {
		t.Fatalf("expression start = %d, want 10", result.Annotations[0].StartUTF16)
	}
	if result.Annotations[1].StartUTF16 <= result.Annotations[0].EndUTF16 {
		t.Fatalf("second annotation overlaps: %+v", result.Annotations)
	}
	if result.Diagnostic.OverlapsDropped != 2 {
		t.Fatalf("overlap diagnostic = %+v", result.Diagnostic)
	}
}

func TestNormalizeCandidatesHandlesRepeatedExactOccurrences(t *testing.T) {
	article := testArticle(t, "Ik leer leren en leer leren.")
	result, err := NormalizeCandidates(&article, []Candidate{
		testCandidate("leren", KindWord, 0, 1, "to learn", true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Annotations) != 1 || result.Annotations[0].SourceText != "leren" {
		t.Fatalf("annotations = %+v", result.Annotations)
	}
	if result.Annotations[0].StartUTF16 != 22 {
		t.Fatalf("second leren offset = %d, want 22", result.Annotations[0].StartUTF16)
	}
}

func TestNormalizeCandidatesEnforcesDensityAndReportsDiagnostic(t *testing.T) {
	words := make([]string, 20)
	candidates := make([]Candidate, 0, len(words))
	for index := range words {
		words[index] = fmt.Sprintf("woord%d", index)
		candidates = append(candidates, testCandidate(words[index], KindWord, 0, 0, "word", true))
	}
	article := testArticle(t, strings.Join(words, " "))
	result, err := NormalizeCandidates(&article, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Annotations) != MaxAnnotationsPer150Words {
		t.Fatalf("retained = %d, want %d", len(result.Annotations), MaxAnnotationsPer150Words)
	}
	shadowCount := 0
	for _, annotation := range result.Annotations {
		if annotation.SuggestShadow {
			shadowCount++
		}
	}
	if shadowCount != MaxShadowsPer150Words || result.Diagnostic.DroppedCandidates != 4 || !result.Diagnostic.BudgetExceeded {
		t.Fatalf("diagnostic = %+v shadowCount=%d", result.Diagnostic, shadowCount)
	}
}

func TestNormalizeCandidatesRejectsInvalidOccurrenceAndFields(t *testing.T) {
	article := testArticle(t, "Een woord.")
	for name, candidate := range map[string]Candidate{
		"missing occurrence": testCandidate("woord", KindWord, 0, 1, "word", true),
		"bad kind":           testCandidate("woord", AnnotationKind("clause"), 0, 0, "word", true),
		"empty key":          testCandidate("woord", KindWord, 0, 0, "", true),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeCandidates(&article, []Candidate{candidate})
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != KindValidation {
				t.Fatalf("error = %v, want typed validation error", err)
			}
		})
	}
}

func testArticle(t *testing.T, body string) Article {
	t.Helper()
	article, err := NewArticle("Test article", body, "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	return article
}

func testCandidate(source string, kind AnnotationKind, block, occurrence int, translation string, shadow bool) Candidate {
	return Candidate{
		BlockIndex:         block,
		SourceText:         source,
		Occurrence:         occurrence,
		Kind:               kind,
		LearningKey:        source,
		PrimaryTranslation: translation,
		Alternatives:       []string{"alternative"},
		MeaningNote:        "meaning",
		UsageNote:          "usage",
		PartsNote:          "parts",
		SuggestShadow:      shadow,
	}
}
