package utility

import "strings"

// Quote-aware scanning for ARRAY transform specs.
//
// An ARRAY transform spec is parsed in two independent places:
//
//  1. at config-load time by extractFieldsFromArrayTransformSpec, to build the SOQL SELECT list;
//  2. at runtime by the expression processor's parseTransformMap, to perform the transform.
//
// Both must tokenise the spec identically. If they disagree, the columns fetched stop matching the
// mappings requested — a bogus column fails the whole query, and a dropped column silently emits
// null. These helpers are therefore the single source of truth for "where do the delimiters sit",
// and both callers use them.
//
// Conventions match parseCustomLogicExpression so the expression language is consistent:
//   - both ' and " open a quoted region
//   - only the SAME quote character closes it, so a " inside '...' is literal text
//   - inside a quoted region a backslash escapes the next byte (\' / \\)

// transformSpecScanner tracks quote state while walking a transform spec so that a delimiter
// appearing inside a quoted literal is not treated as a delimiter.
type transformSpecScanner struct {
	inQuotes  bool
	quoteChar byte
}

// step consumes the byte at index i and returns the next index to read plus whether that byte is
// eligible to be a delimiter — i.e. it sits outside quotes and was not itself consumed as a quote
// marker or as part of an escape sequence.
func (s *transformSpecScanner) step(text string, i int) (next int, delimitable bool) {
	switch char := text[i]; {
	case char == '\\' && s.inQuotes && i+1 < len(text):
		return i + 2, false
	case char == '\'' || char == '"':
		if !s.inQuotes {
			s.inQuotes = true
			s.quoteChar = char
		} else if char == s.quoteChar {
			s.inQuotes = false
		}
		return i + 1, false
	default:
		return i + 1, !s.inQuotes
	}
}

// SplitTransformSpecUnquoted splits text on every occurrence of delim that sits outside a quoted
// region. Quote markers and escape sequences are preserved verbatim in the returned segments, so
// callers can still inspect whether a segment was quoted.
//
// If text ends inside an unbalanced quote the quote-aware pass is discarded and the historical
// naive split is returned instead, so a stray apostrophe in an existing config can never change
// how that config behaves.
func SplitTransformSpecUnquoted(text string, delim byte) []string {
	segments := make([]string, 0, 4)
	var current strings.Builder
	var scanner transformSpecScanner

	for i := 0; i < len(text); {
		next, delimitable := scanner.step(text, i)
		if delimitable && text[i] == delim {
			segments = append(segments, current.String())
			current.Reset()
		} else {
			// Writes the quote marker, the whole escape sequence, or the plain byte verbatim.
			current.WriteString(text[i:next])
		}
		i = next
	}
	segments = append(segments, current.String())

	if scanner.inQuotes {
		return strings.Split(text, string(delim))
	}
	return segments
}

// IndexUnquotedToken returns the index of the first occurrence of token in text that lies outside
// any quoted region, or -1 when there is none. This is what lets `??` be used as data ('??'->tag)
// rather than as the default operator.
func IndexUnquotedToken(text, token string) int {
	var scanner transformSpecScanner

	for i := 0; i < len(text); {
		next, delimitable := scanner.step(text, i)
		if delimitable && strings.HasPrefix(text[i:], token) {
			return i
		}
		i = next
	}
	return -1
}

// IsQuotedLiteral reports whether segment is wrapped in a matched pair of single or double quotes,
// i.e. it is a static literal rather than a field path. Used to keep a literal out of the SOQL
// SELECT list and to decide whether a mapping's left-hand side injects a constant.
func IsQuotedLiteral(segment string) bool {
	trimmed := strings.TrimSpace(segment)
	return len(trimmed) >= 2 &&
		(trimmed[0] == '\'' || trimmed[0] == '"') &&
		trimmed[len(trimmed)-1] == trimmed[0]
}

// UnquoteLiteral strips one matched pair of surrounding single or double quotes. It returns the
// input unchanged when it is not a quoted literal.
func UnquoteLiteral(segment string) string {
	trimmed := strings.TrimSpace(segment)
	if IsQuotedLiteral(trimmed) {
		return trimmed[1 : len(trimmed)-1]
	}
	return segment
}
