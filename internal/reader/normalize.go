package reader

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var foldCase = cases.Fold()

// NormalizeLearningKey applies the persisted learning identity contract:
// Unicode NFC, Unicode case folding, and collapsed Unicode whitespace.
func NormalizeLearningKey(input string) (string, error) {
	if !utf8.ValidString(input) {
		return "", errors.New("learning key must be valid UTF-8")
	}
	value := norm.NFC.String(input)
	value = foldCase.String(value)
	value = norm.NFC.String(value)

	var builder strings.Builder
	spacePending := false
	for _, r := range value {
		if unicode.IsSpace(r) {
			if builder.Len() > 0 {
				spacePending = true
			}
			continue
		}
		if unicode.IsControl(r) {
			return "", errors.New("learning key must not contain control characters")
		}
		if spacePending {
			builder.WriteByte(' ')
			spacePending = false
		}
		builder.WriteRune(r)
	}
	result := builder.String()
	if result == "" {
		return "", errors.New("learning key must not be empty")
	}
	return result, nil
}

// ParseParagraphs splits on one or more blank lines while retaining every
// nonblank paragraph byte, including its internal whitespace and line endings.
func ParseParagraphs(body string) ([]string, error) {
	if body == "" {
		return nil, ErrNoArticleBlocks
	}
	blocks := make([]string, 0, 1)
	lineStart := 0
	blockStart := -1
	lastContentEnd := -1
	for lineStart < len(body) {
		lineEnd, next := lineBounds(body, lineStart)
		line := body[lineStart:lineEnd]
		if strings.TrimSpace(line) == "" {
			if blockStart >= 0 {
				blocks = append(blocks, body[blockStart:lastContentEnd])
				blockStart, lastContentEnd = -1, -1
			}
		} else {
			if blockStart < 0 {
				blockStart = lineStart
			}
			lastContentEnd = lineEnd
		}
		lineStart = next
	}
	if blockStart >= 0 {
		blocks = append(blocks, body[blockStart:lastContentEnd])
	}
	if len(blocks) == 0 {
		return nil, ErrNoArticleBlocks
	}
	return blocks, nil
}

func lineBounds(value string, start int) (end, next int) {
	for index := start; index < len(value); index++ {
		switch value[index] {
		case '\n':
			return index, index + 1
		case '\r':
			if index+1 < len(value) && value[index+1] == '\n' {
				return index, index + 2
			}
			return index, index + 1
		}
	}
	return len(value), len(value)
}

// UTF16Len returns the number of browser string code units in value.
func UTF16Len(value string) int { return len(utf16.Encode([]rune(value))) }

// UTF16Offset returns the browser offset for a byte boundary in value.
func UTF16Offset(value string, byteOffset int) (int, error) {
	if byteOffset < 0 || byteOffset > len(value) || !isRuneBoundary(value, byteOffset) {
		return 0, fmt.Errorf("byte offset %d is outside a UTF-8 rune boundary", byteOffset)
	}
	return UTF16Len(value[:byteOffset]), nil
}

// ByteOffsetFromUTF16 converts a browser offset to a UTF-8 byte boundary.
func ByteOffsetFromUTF16(value string, offset int) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("UTF-16 offset %d is negative", offset)
	}
	units := 0
	for byteOffset, r := range value {
		runeUnits := 1
		if r > 0xffff {
			runeUnits = 2
		}
		if units == offset {
			return byteOffset, nil
		}
		if units+runeUnits > offset {
			return 0, fmt.Errorf("UTF-16 offset %d splits a surrogate pair", offset)
		}
		units += runeUnits
	}
	if units == offset {
		return len(value), nil
	}
	return 0, fmt.Errorf("UTF-16 offset %d exceeds text length %d", offset, units)
}

// TextForUTF16Span extracts a source span using browser offsets and rejects
// malformed or out-of-range boundaries.
func TextForUTF16Span(value string, start, end int) (string, error) {
	if end <= start {
		return "", errors.New("UTF-16 span must have positive length")
	}
	startByte, err := ByteOffsetFromUTF16(value, start)
	if err != nil {
		return "", err
	}
	endByte, err := ByteOffsetFromUTF16(value, end)
	if err != nil {
		return "", err
	}
	return value[startByte:endByte], nil
}

func isRuneBoundary(value string, offset int) bool {
	if offset == 0 || offset == len(value) {
		return true
	}
	return value[offset]&0xc0 != 0x80
}
