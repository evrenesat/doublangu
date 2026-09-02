package reader

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SentenceSpan is one deterministic source sentence inside a paragraph.
// Boundaries are browser UTF-16 code-unit offsets and the text is the exact
// source slice: only inter-sentence whitespace is trimmed from span edges;
// nothing inside a sentence is ever normalized.
type SentenceSpan struct {
	StartUTF16 int
	EndUTF16   int
	SourceText string
}

// sentenceAbbreviations is the normalized abbreviation set after which a
// period never ends a sentence, regardless of what follows. Entries are
// compared case-insensitively against the dotted source word.
var sentenceAbbreviations = []string{
	"dhr", "mevr", "dr", "mr", "prof", "ir", "ing", "st", "etc",
	"bijv", "ca", "nr", "fig", "m.a.w", "o.a", "t.a.v",
}

func isTerminatorPunctuation(r rune) bool {
	return r == '.' || r == '?' || r == '!' || r == '\u2026' // … (ellipsis)
}

// isClosingQuoteOrBracket reports runes that belong to the sentence that
// ended at a preceding terminator. The typographic apostrophe is excluded
// unless a letter follows it (Dutch possessives such as 's ochtends).
func isClosingQuoteOrBracket(r rune) bool {
	switch r {
	case ')', ']', '}', '”', '»', '"', '’':
		return true
	}
	return false
}

func isOpeningQuoteOrBracket(r rune) bool {
	switch r {
	case '(', '[', '{', '„', '«', '“', '‘', '"':
		return true
	}
	return false
}

// SegmentSentences deterministically splits one paragraph into ordered source
// sentences. The splitter operates on runes and returns exact UTF-16 spans.
//
// Rules:
//  1. Only inter-sentence whitespace is trimmed from the returned span edges;
//     text inside a sentence is never normalized.
//  2. '?', '!', and '…' end a sentence. Repeated terminal punctuation and
//     immediately following closing quotes/brackets belong to that sentence.
//  3. A period ends a sentence at paragraph end or when the next non-space
//     source rune begins an uppercase letter, a number, or an opening
//     quote/bracket (closing quotes/brackets directly after the period are
//     consumed first).
//  4. A period does not end a sentence when it is between digits, is part of a
//     repeated initialism (for example "J.S."), or follows the normalized
//     abbreviation set (for example "prof.", "m.a.w.").
//  5. Any remaining non-whitespace paragraph tail is one sentence, so a
//     paragraph without a terminator is a single narration unit.
//
// Leading terminal punctuation never opens an empty sentence; a
// punctuation-only paragraph tail becomes its own short sentence.
func SegmentSentences(text string) ([]SentenceSpan, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("sentence source text must be valid UTF-8")
	}
	type rawSpan struct{ start, end int }
	spans := make([]rawSpan, 0, 1)
	unitStart := 0
	sawNonWhitespace := false

	// flush ends the current unit at byte offset end (exclusive) and appends
	// the trimmed sentence. The next unit starts exactly at end.
	flush := func(end int) {
		start := unitStart
		for start < end {
			r, size := utf8.DecodeRuneInString(text[start:])
			if !unicode.IsSpace(r) {
				break
			}
			start += size
		}
		trailing := end
		for trailing > start {
			r, size := utf8.DecodeLastRuneInString(text[start:trailing])
			if !unicode.IsSpace(r) {
				break
			}
			trailing -= size
		}
		if start < trailing {
			spans = append(spans, rawSpan{start: start, end: trailing})
		}
		unitStart = end
		sawNonWhitespace = false
	}

	for byteIndex := 0; byteIndex < len(text); {
		r, size := utf8.DecodeRuneInString(text[byteIndex:])
		if unicode.IsSpace(r) {
			byteIndex += size
			continue
		}
		if !isTerminatorPunctuation(r) {
			sawNonWhitespace = true
			byteIndex += size
			continue
		}
		if !sawNonWhitespace {
			// Leading terminal punctuation is decoration, never an empty
			// sentence opener; it stays inside the following unit.
			sawNonWhitespace = true
			byteIndex += size
			continue
		}
		ends := r != '.'
		if r == '.' {
			ends = periodEndsSentence(text, byteIndex)
		}
		if !ends {
			byteIndex += size
			continue
		}
		// The terminator ends the sentence; consume repeated terminal
		// punctuation and immediately following closing quotes/brackets.
		end := byteIndex + size
		for end < len(text) {
			next, nextSize := utf8.DecodeRuneInString(text[end:])
			if isTerminatorPunctuation(next) {
				end += nextSize
				continue
			}
			if isClosingQuoteOrBracket(next) {
				if next == '’' {
					// Guard the Dutch apostrophe-possessive: only a closing
					// quote when no letter follows it directly.
					if end+nextSize < len(text) {
						after, afterSize := utf8.DecodeRuneInString(text[end+nextSize:])
						if afterSize > 0 && unicode.IsLetter(after) {
							break
						}
					}
				}
				end += nextSize
				continue
			}
			break
		}
		flush(end)
		// Skip inter-sentence whitespace so the next unit starts at content.
		for end < len(text) {
			next, nextSize := utf8.DecodeRuneInString(text[end:])
			if !unicode.IsSpace(next) {
				break
			}
			end += nextSize
		}
		byteIndex = end
	}
	flush(len(text))

	sentences := make([]SentenceSpan, 0, len(spans))
	for _, span := range spans {
		startUTF16 := UTF16Len(text[:span.start])
		endUTF16 := startUTF16 + UTF16Len(text[span.start:span.end])
		sentences = append(sentences, SentenceSpan{
			StartUTF16: startUTF16,
			EndUTF16:   endUTF16,
			SourceText: text[span.start:span.end],
		})
	}
	return sentences, nil
}

// periodEndsSentence applies rules 3 and 4 at a period whose byte offset is
// dotIndex.
func periodEndsSentence(text string, dotIndex int) bool {
	if dotIndex > 0 && dotIndex+1 < len(text) {
		previous, _ := utf8.DecodeLastRuneInString(text[:dotIndex])
		next, _ := utf8.DecodeRuneInString(text[dotIndex+1:])
		if unicode.IsNumber(previous) && unicode.IsNumber(next) {
			// Between digits: "04.00 uur", "3.14".
			return false
		}
	}
	if dottedWordStopsSentence(text, dotIndex) {
		// Abbreviation or part of a repeated initialism.
		return false
	}
	// Look past immediately following closing quotes/brackets, then whitespace.
	next := dotIndex + 1
	for next < len(text) {
		r, size := utf8.DecodeRuneInString(text[next:])
		if !isClosingQuoteOrBracket(r) {
			break
		}
		next += size
	}
	for next < len(text) {
		r, size := utf8.DecodeRuneInString(text[next:])
		if !unicode.IsSpace(r) {
			break
		}
		next += size
	}
	if next >= len(text) {
		return true // Paragraph-final period.
	}
	r, _ := utf8.DecodeRuneInString(text[next:])
	if unicode.IsUpper(r) || unicode.IsNumber(r) || isOpeningQuoteOrBracket(r) {
		return true
	}
	return false
}

// dottedWordStopsSentence reports whether the period ends a dotted word that
// must not terminate the sentence: a repeated initialism or an abbreviation
// from the normalized set. It also recognizes an initialism that continues
// after the period (the "J." in "J.S. Bach").
func dottedWordStopsSentence(text string, dotIndex int) bool {
	token := dottedTokenBefore(text, dotIndex)
	if token != "" {
		for _, abbreviation := range sentenceAbbreviations {
			if strings.EqualFold(token, abbreviation) {
				return true
			}
		}
		// Repeated initialism: every group is a single letter ("J.S.", "U.S.").
		groups := 0
		allSingle := true
		for _, part := range strings.Split(token, ".") {
			if part == "" {
				continue
			}
			groups++
			if utf8.RuneCountInString(part) != 1 {
				allSingle = false
			}
		}
		if groups >= 2 && allSingle {
			return true
		}
	}
	// Initialism continuation: the next non-space rune is a letter followed
	// by another period ("J." inside "J.S.").
	next := dotIndex + 1
	for next < len(text) {
		r, size := utf8.DecodeRuneInString(text[next:])
		if !unicode.IsSpace(r) {
			break
		}
		next += size
	}
	if next < len(text) {
		letter, letterSize := utf8.DecodeRuneInString(text[next:])
		if unicode.IsLetter(letter) {
			after := next + letterSize
			for after < len(text) {
				r, size := utf8.DecodeRuneInString(text[after:])
				if !unicode.IsSpace(r) {
					break
				}
				after += size
			}
			if after < len(text) {
				r, _ := utf8.DecodeRuneInString(text[after:])
				if r == '.' {
					return true
				}
			}
		}
	}
	return false
}

// dottedTokenBefore returns the maximal run of letters and periods that ends
// just before dotIndex (the abbreviation or initialism candidate).
func dottedTokenBefore(text string, dotIndex int) string {
	end := dotIndex
	for end > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:end])
		if r != '.' && !unicode.IsLetter(r) {
			break
		}
		end -= size
	}
	return text[end:dotIndex]
}
