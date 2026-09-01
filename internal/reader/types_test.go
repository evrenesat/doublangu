package reader

import (
	_ "embed"
	"errors"
	"strings"
	"testing"
)

// The checked-in sample is test-only fixture data; it is never loaded by the
// application or shown as a default article.
//
//go:embed testdata/article-dutch.txt
var dutchSampleArticle string

func TestCheckedInDutchSampleParsesAsArticleBlocks(t *testing.T) {
	article, err := NewArticle("Sample", dutchSampleArticle, "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(article.Blocks) != 3 || article.Blocks[1].SourceText == "" {
		t.Fatalf("sample blocks = %+v", article.Blocks)
	}
}

func TestParseParagraphsPreservesInternalTextAndDropsBlankEdges(t *testing.T) {
	body := "\n \r\n  Eerste regel  \nTweede regel\n\n\t\nDerde alinea\n \n"
	blocks, err := ParseParagraphs(body)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"  Eerste regel  \nTweede regel", "Derde alinea"}; !equalStrings(blocks, want) {
		t.Fatalf("blocks = %#v, want %#v", blocks, want)
	}
}

func TestNewArticleValidatesTitleAndBodyLimits(t *testing.T) {
	if _, err := NewArticle("", "tekst", "nl", "en"); err == nil {
		t.Fatal("empty title accepted")
	}
	if _, err := NewArticle(strings.Repeat("a", MaxArticleTitleScalars+1), "tekst", "nl", "en"); err == nil {
		t.Fatal("oversized title accepted")
	}
	if _, err := NewArticle("Titel", strings.Repeat("x", MaxArticleBodyBytes+1), "nl", "en"); !errors.Is(err, ErrArticleBodyTooLarge) {
		t.Fatalf("body error = %v, want ErrArticleBodyTooLarge", err)
	}
	article, err := NewArticle(" Titel ", "tekst", "NL-nl", "EN-us")
	if err != nil {
		t.Fatal(err)
	}
	if article.Title != "Titel" || article.SourceLanguage != "nl-NL" || article.TargetLanguage != "en-US" {
		t.Fatalf("article = %+v", article)
	}
}

func TestAnnotatorInputEnforcesRequestLimitWithoutTruncating(t *testing.T) {
	article, err := NewArticle("Titel", strings.Repeat("woord ", 3500), "nl", "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(article.Blocks[0].SourceText) <= MaxEnrichmentBodyBytes {
		t.Fatal("test body did not exceed enrichment limit")
	}
	_, err = article.AnnotatorInput()
	if !errors.Is(err, ErrEnrichmentBodyTooLarge) {
		t.Fatalf("input error = %v, want ErrEnrichmentBodyTooLarge", err)
	}
	if !strings.HasPrefix(article.Blocks[0].SourceText, "woord woord") {
		t.Fatal("source text was unexpectedly truncated")
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
