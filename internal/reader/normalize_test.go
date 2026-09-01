package reader

import (
	"errors"
	"testing"
)

func TestNormalizeLearningKeyUsesNFCCaseFoldAndUnicodeWhitespace(t *testing.T) {
	got, err := NormalizeLearningKey("  TOT\u00a0RUST\tKOMEN  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "tot rust komen" {
		t.Fatalf("key = %q", got)
	}
	got, err = NormalizeLearningKey("Cafe\u0301")
	if err != nil || got != "café" {
		t.Fatalf("NFC key = %q err=%v", got, err)
	}
	if _, err := NormalizeLearningKey("\u2003\u2009"); err == nil {
		t.Fatal("whitespace-only key accepted")
	}
	if _, err := NormalizeLearningKey("woord\x00"); err == nil {
		t.Fatal("control character accepted")
	}
}

func TestUTF16OffsetsHandleNonBMPText(t *testing.T) {
	value := "😀 leren"
	if UTF16Len(value) != 8 {
		t.Fatalf("UTF16Len = %d, want 8", UTF16Len(value))
	}
	start, err := UTF16Offset(value, len("😀 "))
	if err != nil || start != 3 {
		t.Fatalf("start = %d err=%v, want 3", start, err)
	}
	text, err := TextForUTF16Span(value, 3, 8)
	if err != nil || text != "leren" {
		t.Fatalf("span = %q err=%v", text, err)
	}
	if _, err := TextForUTF16Span(value, 1, 3); err == nil {
		t.Fatal("surrogate-splitting span accepted")
	}
	if _, err := ByteOffsetFromUTF16(value, 1); err == nil {
		t.Fatal("surrogate-splitting offset accepted")
	}
}

func TestParseParagraphsRejectsOnlyBlankText(t *testing.T) {
	_, err := ParseParagraphs(" \n\t\r\n")
	if !errors.Is(err, ErrNoArticleBlocks) {
		t.Fatalf("error = %v, want ErrNoArticleBlocks", err)
	}
}
