package client

import (
	"regexp"
	"strings"
)

// This file implements null/blank-aware pruning of SOQL WHERE conditions.
//
// Motivation
// ----------
// A query object's additional_conditions may reference several placeholder-driven fields,
// e.g. `WHERE CreatedDate >= LAST_N_DAYS:30 AND (PAN__c = <a> OR Mobile = <b> OR Email = <c>)`.
// Historically, if ANY placeholder resolved to null/blank the whole subquery was dropped —
// even under OR semantics where the surviving branches would have matched. This module lets
// us prune only the branches whose placeholders are empty, using three-valued (Kleene) logic:
//
//   - A comparison whose placeholder resolves to empty (nil/blank) is UNKNOWN.
//   - OR:  drop UNKNOWN operands, keep survivors; if all operands are UNKNOWN the group is UNKNOWN.
//   - AND: if any operand is UNKNOWN the whole conjunction is UNKNOWN (cannot be safely dropped —
//          dropping an AND term would broaden the result set).
//   - If the whole clause reduces to UNKNOWN -> skip the query (never emit an identity-less filter).
//
// Safety / future-proofing
// ------------------------
// The parser deliberately supports a bounded grammar (comparisons as opaque leaves, AND/OR,
// parentheses, quoted string literals, and a trailing ORDER BY / GROUP BY / LIMIT / OFFSET tail).
// Anything it is not fully confident about — unbalanced parentheses, an unexpected shape, or any
// internal panic — yields prunedFallback so the caller keeps its existing behavior. It never
// rewrites a comparison's internals; it only drops whole branches and re-groups survivors, so a
// leaf it does not "understand" (a new operator, function, date literal, IN-list, NOT (...), etc.)
// is preserved verbatim as long as its placeholders resolve.

// pruneDecision is the outcome of attempting to prune a WHERE clause.
type pruneDecision int

const (
	// prunedOK: the clause was reduced successfully; use the returned clause.
	prunedOK pruneDecision = iota
	// prunedSkip: the clause has no satisfiable identity filter after pruning; skip the query.
	prunedSkip
	// prunedFallback: the clause is outside the supported grammar / could not be parsed with
	// confidence; the caller should retain its existing (pre-pruning) behavior.
	prunedFallback
)

// maxParseDepth bounds recursion so a pathological input cannot blow the stack.
const maxParseDepth = 64

var (
	// compositeRefRegex matches a composite reference @{object_query.records[0].field}. The object
	// segment is non-greedy so custom-object/label names with underscores are captured correctly.
	compositeRefRegex = regexp.MustCompile(`@\{(.+?)_query\.records\[0\]\.([^}]+)\}`)
	// placeholderRe matches any composite placeholder marker, e.g. @{lead_query.records[0].pan__c}.
	placeholderRe = regexp.MustCompile(`@\{[^}]*\}`)
	// trailingClauseKeywords are clauses that follow the boolean WHERE conditions and must be
	// preserved verbatim (never treated as part of the boolean tree).
	trailingClauseKeywords = []string{"ORDER BY", "GROUP BY", "LIMIT", "OFFSET"}
)

// emptyClassifier reports whether a composite placeholder (@{...}) resolves to an empty
// (nil/blank) value. ok is false when the placeholder cannot be classified with confidence,
// which forces the pruner to fall back rather than guess.
type emptyClassifier func(placeholder string) (empty bool, ok bool)

// condNode is a node in the parsed boolean condition tree.
type condNode interface{ isCondNode() }

type leafNode struct{ text string } // an opaque comparison predicate (kept verbatim)
type andNode struct{ children []condNode }
type orNode struct{ children []condNode }

func (leafNode) isCondNode() {}
func (andNode) isCondNode()  {}
func (orNode) isCondNode()   {}

// pruneWhereConditions attempts to prune empty-placeholder branches from a WHERE-prefixed
// condition string. The input still contains @{...} placeholders; survivors are returned with
// their placeholders intact so the caller can substitute values as usual.
func pruneWhereConditions(conditions string, classify emptyClassifier) (pruned string, decision pruneDecision) {
	// Any unexpected failure degrades gracefully to fallback (caller keeps current behavior).
	defer func() {
		if r := recover(); r != nil {
			pruned, decision = "", prunedFallback
		}
	}()

	rest, ok := stripLeadingWhere(conditions)
	if !ok {
		return "", prunedFallback
	}

	body, trailing := splitTrailingClause(rest)
	if strings.TrimSpace(body) == "" {
		return "", prunedFallback
	}

	root, ok := parseOrExpr(body, 0)
	if !ok {
		return "", prunedFallback
	}

	text, known, ok := reduceNode(root, classify)
	if !ok {
		return "", prunedFallback
	}
	if !known {
		// Whole clause is UNKNOWN -> no satisfiable identity filter -> skip.
		return "", prunedSkip
	}

	var sb strings.Builder
	sb.WriteString("WHERE ")
	sb.WriteString(text)
	if trailing != "" {
		sb.WriteString(" ")
		sb.WriteString(trailing)
	}
	return sb.String(), prunedOK
}

// hasEmptyComposite reports whether the string contains at least one placeholder that the
// classifier confidently marks as empty. Used as an engagement gate.
func hasEmptyComposite(s string, classify emptyClassifier) bool {
	for _, m := range placeholderRe.FindAllString(s, -1) {
		if empty, ok := classify(m); ok && empty {
			return true
		}
	}
	return false
}

// stripLeadingWhere removes a leading WHERE keyword (case-insensitive) and returns the rest.
func stripLeadingWhere(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if !matchesKeywordAt(t, 0, "WHERE") {
		return "", false
	}
	return strings.TrimSpace(t[len("WHERE"):]), true
}

// splitTrailingClause splits off a trailing ORDER BY / GROUP BY / LIMIT / OFFSET tail (at the
// top level, outside quotes and parentheses). The tail is preserved verbatim.
func splitTrailingClause(s string) (body string, trailing string) {
	depth := 0
	inQuote := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote {
			if ch == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if ch == '\'' {
				inQuote = false
			}
			continue
		}
		switch ch {
		case '\'':
			inQuote = true
			continue
		case '(':
			depth++
			continue
		case ')':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 {
			for _, kw := range trailingClauseKeywords {
				if matchesKeywordAt(s, i, kw) {
					return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
				}
			}
		}
	}
	return strings.TrimSpace(s), ""
}

// parseOrExpr splits on top-level OR (lowest precedence).
func parseOrExpr(s string, depth int) (condNode, bool) {
	if depth > maxParseDepth {
		return nil, false
	}
	parts, ok := splitTopLevel(s, "OR")
	if !ok {
		return nil, false
	}
	if len(parts) == 1 {
		return parseAndExpr(parts[0], depth)
	}
	children := make([]condNode, 0, len(parts))
	for _, p := range parts {
		ch, ok := parseAndExpr(p, depth+1)
		if !ok {
			return nil, false
		}
		children = append(children, ch)
	}
	return orNode{children: children}, true
}

// parseAndExpr splits on top-level AND (binds tighter than OR).
func parseAndExpr(s string, depth int) (condNode, bool) {
	if depth > maxParseDepth {
		return nil, false
	}
	parts, ok := splitTopLevel(s, "AND")
	if !ok {
		return nil, false
	}
	if len(parts) == 1 {
		return parseFactor(parts[0], depth)
	}
	children := make([]condNode, 0, len(parts))
	for _, p := range parts {
		ch, ok := parseFactor(p, depth+1)
		if !ok {
			return nil, false
		}
		children = append(children, ch)
	}
	return andNode{children: children}, true
}

// parseFactor handles a parenthesized group or an opaque comparison leaf.
func parseFactor(s string, depth int) (condNode, bool) {
	if depth > maxParseDepth {
		return nil, false
	}
	t := strings.TrimSpace(s)
	if t == "" {
		return nil, false
	}
	// Treat NOT (...) as an opaque leaf: negation is preserved verbatim and never split, so a
	// NOT branch is kept when its placeholders resolve and pruned (as UNKNOWN) when they don't.
	if matchesKeywordAt(t, 0, "NOT") {
		return leafNode{text: t}, true
	}
	// A wholly-parenthesized group -> strip the outer parens and recurse.
	if t[0] == '(' {
		inner, ok := strippedWholeParen(t)
		if !ok {
			return nil, false
		}
		return parseOrExpr(inner, depth+1)
	}
	return leafNode{text: t}, true
}

// splitTopLevel splits s on the given boolean keyword at parenthesis depth 0, outside quotes,
// matching whole words case-insensitively. An empty segment (dangling operator) fails.
func splitTopLevel(s, keyword string) ([]string, bool) {
	var parts []string
	depth := 0
	inQuote := false
	start := 0
	klen := len(keyword)
	i := 0
	for i < len(s) {
		ch := s[i]
		if inQuote {
			if ch == '\\' && i+1 < len(s) {
				i += 2
				continue
			}
			if ch == '\'' {
				inQuote = false
			}
			i++
			continue
		}
		switch ch {
		case '\'':
			inQuote = true
			i++
			continue
		case '(':
			depth++
			i++
			continue
		case ')':
			depth--
			if depth < 0 {
				return nil, false
			}
			i++
			continue
		}
		if depth == 0 && matchesKeywordAt(s, i, keyword) {
			seg := strings.TrimSpace(s[start:i])
			if seg == "" {
				return nil, false
			}
			parts = append(parts, seg)
			i += klen
			start = i
			continue
		}
		i++
	}
	if inQuote || depth != 0 {
		return nil, false
	}
	last := strings.TrimSpace(s[start:])
	if last == "" {
		// Nothing after a trailing operator (or an all-empty string).
		if len(parts) == 0 {
			return nil, false
		}
		return nil, false
	}
	parts = append(parts, last)
	return parts, true
}

// strippedWholeParen returns the inner content when t is wholly wrapped by a single matching
// pair of parentheses (e.g. "(A OR B)"); ok is false otherwise (e.g. "(A) OR (B)") or unbalanced.
func strippedWholeParen(t string) (string, bool) {
	if len(t) == 0 || t[0] != '(' {
		return "", false
	}
	depth := 0
	inQuote := false
	for i := 0; i < len(t); i++ {
		ch := t[i]
		if inQuote {
			if ch == '\\' && i+1 < len(t) {
				i++
				continue
			}
			if ch == '\'' {
				inQuote = false
			}
			continue
		}
		switch ch {
		case '\'':
			inQuote = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				if i == len(t)-1 {
					return strings.TrimSpace(t[1:i]), true
				}
				return "", false // closes before the end -> not wholly wrapped
			}
		}
	}
	return "", false // unbalanced
}

// reduceNode applies three-valued reduction. It returns the emitted SOQL text, whether the node
// is "known" (satisfiable / retained) and whether classification succeeded (ok=false -> fallback).
func reduceNode(n condNode, classify emptyClassifier) (text string, known bool, ok bool) {
	switch v := n.(type) {
	case leafNode:
		empty, cok := leafIsEmpty(v.text, classify)
		if !cok {
			return "", false, false
		}
		if empty {
			return "", false, true // UNKNOWN
		}
		return v.text, true, true
	case andNode:
		kept := make([]string, 0, len(v.children))
		for _, ch := range v.children {
			t, k, cok := reduceNode(ch, classify)
			if !cok {
				return "", false, false
			}
			if !k {
				// AND with an UNKNOWN operand cannot be safely satisfied -> UNKNOWN.
				return "", false, true
			}
			kept = append(kept, wrapChild(ch, t))
		}
		if len(kept) == 0 {
			return "", false, true
		}
		return strings.Join(kept, " AND "), true, true
	case orNode:
		kept := make([]string, 0, len(v.children))
		for _, ch := range v.children {
			t, k, cok := reduceNode(ch, classify)
			if !cok {
				return "", false, false
			}
			if !k {
				continue // drop UNKNOWN OR branch
			}
			kept = append(kept, wrapChild(ch, t))
		}
		if len(kept) == 0 {
			return "", false, true // all branches UNKNOWN -> UNKNOWN
		}
		return strings.Join(kept, " OR "), true, true
	}
	return "", false, false
}

// wrapChild parenthesizes a composite child so precedence is preserved when nested under a parent.
// Extra parentheses are always semantically safe in SOQL.
func wrapChild(child condNode, text string) string {
	switch child.(type) {
	case andNode, orNode:
		return "(" + text + ")"
	}
	return text
}

// leafIsEmpty reports whether an opaque comparison leaf contains any empty placeholder. A leaf
// with no placeholder is a static predicate and is never empty. ok=false forces a fallback.
func leafIsEmpty(text string, classify emptyClassifier) (empty bool, ok bool) {
	for _, m := range placeholderRe.FindAllString(text, -1) {
		e, cok := classify(m)
		if !cok {
			return false, false
		}
		if e {
			return true, true
		}
	}
	return false, true
}

// matchesKeywordAt reports whether keyword occurs at position i in s as a whole word
// (case-insensitive), bounded by non-word characters (or string edges).
func matchesKeywordAt(s string, i int, keyword string) bool {
	klen := len(keyword)
	if i < 0 || i+klen > len(s) {
		return false
	}
	if !strings.EqualFold(s[i:i+klen], keyword) {
		return false
	}
	if i > 0 && isWordByte(s[i-1]) {
		return false
	}
	if i+klen < len(s) && isWordByte(s[i+klen]) {
		return false
	}
	return true
}

// isWordByte reports whether b is a SOQL identifier character (letter, digit, or underscore).
func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}
