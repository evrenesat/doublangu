package reader

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestSegmentSentences(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		sentences []string
	}{
		{
			name:      "two plain sentences",
			text:      "Hallo wereld. Dit is een test.",
			sentences: []string{"Hallo wereld.", "Dit is een test."},
		},
		{
			name:      "Dutch quotes close into the ending sentence",
			text:      "Hij zei: „Ik kom morgen.” Zij ging weg.",
			sentences: []string{"Hij zei: „Ik kom morgen.”", "Zij ging weg."},
		},
		{
			name:      "closing bracket after period",
			text:      "Dat is zo (zie bijlage). Daarna kwam hij.",
			sentences: []string{"Dat is zo (zie bijlage).", "Daarna kwam hij."},
		},
		{
			name:      "ellipsis ends a sentence",
			text:      "Hij aarzelde … toen liep hij door.",
			sentences: []string{"Hij aarzelde …", "toen liep hij door."},
		},
		{
			name:      "paragraph-final ellipsis",
			text:      "Wacht even…",
			sentences: []string{"Wacht even…"},
		},
		{
			name:      "ASCII trailing periods stay one unit",
			text:      "Hij liep door...",
			sentences: []string{"Hij liep door..."},
		},
		{
			name:      "clock time keeps the sentence together",
			text:      "De trein vertrekt om 04.00 uur. Hij komt later.",
			sentences: []string{"De trein vertrekt om 04.00 uur.", "Hij komt later."},
		},
		{
			name:      "decimal number",
			text:      "Het kost 3.14 euro en is goed.",
			sentences: []string{"Het kost 3.14 euro en is goed."},
		},
		{
			name:      "repeated initials do not split",
			text:      "J.S. Bach schreef muziek. Hij was beroemd.",
			sentences: []string{"J.S. Bach schreef muziek.", "Hij was beroemd."},
		},
		{
			name:      "single initial before a name",
			text:      "Ik ken J. van der Berg niet. Toch?",
			sentences: []string{"Ik ken J. van der Berg niet.", "Toch?"},
		},
		{
			name:      "abbreviations do not split",
			text:      "Prof. Jansen komt om 10 uur. Dhr. Pieters ook.",
			sentences: []string{"Prof. Jansen komt om 10 uur.", "Dhr. Pieters ook."},
		},
		{
			name:      "dotted abbreviations",
			text:      "Het is simpel, m.a.w. het werkt. Toch?",
			sentences: []string{"Het is simpel, m.a.w. het werkt.", "Toch?"},
		},
		{
			name:      "abbreviation mid sentence before lowercase",
			text:      "Het is ca. 1900 gebouwd. Mooi.",
			sentences: []string{"Het is ca. 1900 gebouwd.", "Mooi."},
		},
		{
			name:      "paragraph-final period after a number",
			text:      "Hij koos 4.",
			sentences: []string{"Hij koos 4."},
		},
		{
			name:      "paragraph-final abbreviation is one unit",
			text:      "Hij komt etc.",
			sentences: []string{"Hij komt etc."},
		},
		{
			name:      "exclamation and question marks",
			text:      "Wat?! Kom hier. Echt?",
			sentences: []string{"Wat?!", "Kom hier.", "Echt?"},
		},
		{
			name:      "punctuation-only tail after a terminator",
			text:      "Zie je? …",
			sentences: []string{"Zie je?", "…"},
		},
		{
			name:      "leading ellipsis decorates the next unit",
			text:      "…hij zei niets meer.",
			sentences: []string{"…hij zei niets meer."},
		},
		{
			name:      "no terminator is one narration unit",
			text:      "Geen punt aan het eind",
			sentences: []string{"Geen punt aan het eind"},
		},
		{
			name:      "period then opening quote",
			text:      "Hij zei. „Toen ging hij weg.”",
			sentences: []string{"Hij zei.", "„Toen ging hij weg.”"},
		},
		{
			name:      "newline between sentences",
			text:      "Hij kwam.\nToen ging hij.",
			sentences: []string{"Hij kwam.", "Toen ging hij."},
		},
		{
			name:      "internal whitespace is preserved",
			text:      "Eerste regel.\n  Tweede regel.",
			sentences: []string{"Eerste regel.", "Tweede regel."},
		},
		{
			name:      "Dutch punctuation-only words",
			text:      "„Kom binnen.” Hij knikte.",
			sentences: []string{"„Kom binnen.”", "Hij knikte."},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SegmentSentences(test.text)
			if err != nil {
				t.Fatalf("SegmentSentences(%q): %v", test.text, err)
			}
			texts := make([]string, len(got))
			for index, sentence := range got {
				texts[index] = sentence.SourceText
			}
			if !reflect.DeepEqual(texts, test.sentences) {
				t.Errorf("SegmentSentences(%q) = %#v, want %#v", test.text, texts, test.sentences)
			}
		})
	}
}

func TestSegmentSentencesOffsets(t *testing.T) {
	// "Hij lacht 😀. Echt." — the emoji is a four-byte rune that occupies two
	// UTF-16 code units, so byte and UTF-16 offsets must diverge.
	text := "Hij lacht 😀. Echt."
	sentences, err := SegmentSentences(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(sentences) != 2 {
		t.Fatalf("got %d sentences, want 2", len(sentences))
	}
	firstUnits := len(utf16.Encode([]rune("Hij lacht 😀.")))
	if sentences[0].StartUTF16 != 0 || sentences[0].EndUTF16 != firstUnits {
		t.Errorf("first sentence offsets = [%d,%d), want [0,%d)", sentences[0].StartUTF16, sentences[0].EndUTF16, firstUnits)
	}
	secondStart := sentences[1].StartUTF16
	if want := firstUnits + 1; secondStart != want {
		t.Errorf("second sentence start = %d, want %d", secondStart, want)
	}
	if sentences[1].EndUTF16 != secondStart+len(utf16.Encode([]rune("Echt."))) {
		t.Errorf("second sentence end = %d, want %d", sentences[1].EndUTF16, secondStart+len(utf16.Encode([]rune("Echt."))))
	}
	if sentences[0].SourceText != "Hij lacht 😀." || sentences[1].SourceText != "Echt." {
		t.Errorf("sentence texts = %q, %q", sentences[0].SourceText, sentences[1].SourceText)
	}
}

func TestSegmentSentencesCoverage(t *testing.T) {
	// Sentences must cover every non-whitespace source rune exactly once: the
	// gaps between spans are whitespace only, and no source text is lost.
	texts := []string{
		"Hallo wereld. Dit is een test.",
		"„Kom binnen.” Hij knikte. Wat?",
		"De trein vertrekt om 04.00 uur. Hij komt later.",
		"Hij lacht 😀. Echt. …",
		"één zin zonder punt",
	}
	for _, text := range texts {
		sentences, err := SegmentSentences(text)
		if err != nil {
			t.Fatalf("SegmentSentences(%q): %v", text, err)
		}
		previousEndByte := 0
		var covered strings.Builder
		for _, sentence := range sentences {
			if sentence.StartUTF16 < 0 || sentence.EndUTF16 <= sentence.StartUTF16 {
				t.Errorf("sentence span [%d,%d) in %q is invalid", sentence.StartUTF16, sentence.EndUTF16, text)
			}
			startByte := utf16ByteIndex(text, sentence.StartUTF16)
			endByte := utf16ByteIndex(text, sentence.EndUTF16)
			if startByte < previousEndByte || startByte > len(text) || endByte > len(text) {
				t.Errorf("sentence span [%d,%d) in %q is out of order", sentence.StartUTF16, sentence.EndUTF16, text)
				continue
			}
			if gap := text[previousEndByte:startByte]; strings.TrimSpace(gap) != "" {
				t.Errorf("gap between sentences in %q is not whitespace: %q", text, gap)
			}
			covered.WriteString(text[startByte:endByte])
			previousEndByte = endByte
		}
		if tail := text[previousEndByte:]; strings.TrimSpace(tail) != "" {
			t.Errorf("sentences in %q leave uncovered tail %q", text, tail)
		}
	}
}

// utf16ByteIndex converts a UTF-16 code-unit offset back into a byte offset.
func utf16ByteIndex(value string, utf16Offset int) int {
	byteIndex := 0
	units := 0
	for byteIndex < len(value) && units < utf16Offset {
		r, size := utf8.DecodeRuneInString(value[byteIndex:])
		if size == 0 {
			break
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
		byteIndex += size
	}
	return byteIndex
}
