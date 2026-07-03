// Package expr parses the bounded OSI metric expression grammar:
//
//	MetricExpr := Term ( '/' Denom )?
//	Denom      := Term | 'NULLIF' '(' Term ',' Number ')'
//	Term       := AggFunc '(' [ 'DISTINCT' ] Scalar ')'
//	AggFunc    := SUM | COUNT | AVG | MIN | MAX
//
// Scalar is a SQL scalar expression whose column references are written as
// dataset.field. Anything outside this grammar is rejected at validation
// time, never at query time.
package expr

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// FieldRef is a dataset.field reference inside a scalar expression.
type FieldRef struct {
	Dataset string `json:"dataset"`
	Field   string `json:"field"`
}

func (r FieldRef) String() string { return r.Dataset + "." + r.Field }

// AggTerm is one aggregation over a scalar expression.
type AggTerm struct {
	Func     string     `json:"func"` // SUM, COUNT, AVG, MIN, MAX
	Distinct bool       `json:"distinct,omitempty"`
	Scalar   string     `json:"scalar"` // raw scalar with dataset.field refs
	Refs     []FieldRef `json:"refs"`
}

// MetricExpr is a parsed metric: an aggregation or a ratio of two.
type MetricExpr struct {
	Numerator   AggTerm  `json:"numerator"`
	Denominator *AggTerm `json:"denominator,omitempty"`
}

// Datasets returns the sorted, unique datasets referenced by the expression.
func (m MetricExpr) Datasets() []string {
	set := map[string]bool{}
	for _, r := range m.Numerator.Refs {
		set[r.Dataset] = true
	}
	if m.Denominator != nil {
		for _, r := range m.Denominator.Refs {
			set[r.Dataset] = true
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

var aggFuncs = map[string]bool{"SUM": true, "COUNT": true, "AVG": true, "MIN": true, "MAX": true}

type parser struct {
	in  string
	pos int
}

// Parse parses a metric expression string.
func Parse(s string) (MetricExpr, error) {
	p := &parser{in: s}
	num, err := p.parseTerm()
	if err != nil {
		return MetricExpr{}, err
	}
	p.skipSpace()
	if p.eof() {
		return MetricExpr{Numerator: num}, nil
	}
	if p.in[p.pos] != '/' {
		return MetricExpr{}, fmt.Errorf("metric expression %q: unexpected %q at offset %d, expected '/' or end", s, p.in[p.pos:], p.pos)
	}
	p.pos++
	den, err := p.parseDenom()
	if err != nil {
		return MetricExpr{}, err
	}
	p.skipSpace()
	if !p.eof() {
		return MetricExpr{}, fmt.Errorf("metric expression %q: trailing input %q", s, p.in[p.pos:])
	}
	return MetricExpr{Numerator: num, Denominator: &den}, nil
}

func (p *parser) parseDenom() (AggTerm, error) {
	p.skipSpace()
	save := p.pos
	ident := p.readIdent()
	if strings.EqualFold(ident, "NULLIF") {
		p.skipSpace()
		if !p.consume('(') {
			return AggTerm{}, fmt.Errorf("expected '(' after NULLIF at offset %d", p.pos)
		}
		t, err := p.parseTerm()
		if err != nil {
			return AggTerm{}, err
		}
		p.skipSpace()
		if !p.consume(',') {
			return AggTerm{}, fmt.Errorf("expected ',' in NULLIF at offset %d", p.pos)
		}
		p.skipSpace()
		p.readNumber()
		p.skipSpace()
		if !p.consume(')') {
			return AggTerm{}, fmt.Errorf("expected ')' closing NULLIF at offset %d", p.pos)
		}
		return t, nil
	}
	p.pos = save
	return p.parseTerm()
}

func (p *parser) parseTerm() (AggTerm, error) {
	p.skipSpace()
	fn := strings.ToUpper(p.readIdent())
	if !aggFuncs[fn] {
		return AggTerm{}, fmt.Errorf("expected aggregate function (SUM/COUNT/AVG/MIN/MAX) at offset %d, got %q", p.pos, fn)
	}
	p.skipSpace()
	if !p.consume('(') {
		return AggTerm{}, fmt.Errorf("expected '(' after %s at offset %d", fn, p.pos)
	}
	inner, err := p.readBalanced()
	if err != nil {
		return AggTerm{}, err
	}
	inner = strings.TrimSpace(inner)
	distinct := false
	if up := strings.ToUpper(inner); strings.HasPrefix(up, "DISTINCT") && len(inner) > 8 && unicode.IsSpace(rune(inner[8])) {
		distinct = true
		inner = strings.TrimSpace(inner[8:])
	}
	if inner == "" {
		return AggTerm{}, fmt.Errorf("%s(...) has empty argument", fn)
	}
	refs, err := ScanRefs(inner)
	if err != nil {
		return AggTerm{}, err
	}
	if len(refs) == 0 && inner != "*" {
		return AggTerm{}, fmt.Errorf("%s(%s): no dataset.field reference found; metric scalars must reference fields as dataset.field", fn, inner)
	}
	return AggTerm{Func: fn, Distinct: distinct, Scalar: inner, Refs: refs}, nil
}

// readBalanced reads up to the ')' matching the already-consumed '(',
// respecting nested parens and single-quoted strings.
func (p *parser) readBalanced() (string, error) {
	depth := 1
	start := p.pos
	for p.pos < len(p.in) {
		c := p.in[p.pos]
		switch c {
		case '\'':
			p.pos++
			for p.pos < len(p.in) && p.in[p.pos] != '\'' {
				p.pos++
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				out := p.in[start:p.pos]
				p.pos++
				return out, nil
			}
		}
		p.pos++
	}
	return "", fmt.Errorf("unbalanced parentheses in %q", p.in)
}

func (p *parser) skipSpace() {
	for p.pos < len(p.in) && unicode.IsSpace(rune(p.in[p.pos])) {
		p.pos++
	}
}

func (p *parser) eof() bool { return p.pos >= len(p.in) }

func (p *parser) consume(c byte) bool {
	if p.pos < len(p.in) && p.in[p.pos] == c {
		p.pos++
		return true
	}
	return false
}

func (p *parser) readIdent() string {
	p.skipSpace()
	start := p.pos
	for p.pos < len(p.in) && (isIdentChar(p.in[p.pos])) {
		p.pos++
	}
	return p.in[start:p.pos]
}

func (p *parser) readNumber() string {
	start := p.pos
	for p.pos < len(p.in) && (p.in[p.pos] >= '0' && p.in[p.pos] <= '9' || p.in[p.pos] == '.' || p.in[p.pos] == '-') {
		p.pos++
	}
	return p.in[start:p.pos]
}

func isIdentChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// sqlKeywords are identifiers never treated as column references when
// scanning or qualifying scalar expressions.
var sqlKeywords = map[string]bool{
	"AND": true, "OR": true, "NOT": true, "NULL": true, "CASE": true,
	"WHEN": true, "THEN": true, "ELSE": true, "END": true, "IN": true,
	"IS": true, "LIKE": true, "BETWEEN": true, "DISTINCT": true,
	"TRUE": true, "FALSE": true, "INTERVAL": true, "AS": true,
	"DIV": true, "MOD": true,
}

// ScanRefs extracts dataset.field references from a scalar expression.
// Bare identifiers that are not followed by '(' (function calls) and not
// keywords are rejected: inside metric expressions every column must be
// dataset-qualified so the planner can resolve the join graph.
func ScanRefs(scalar string) ([]FieldRef, error) {
	var refs []FieldRef
	seen := map[string]bool{}
	i := 0
	for i < len(scalar) {
		c := scalar[i]
		if c == '\'' { // skip string literal
			i++
			for i < len(scalar) && scalar[i] != '\'' {
				i++
			}
			i++
			continue
		}
		if !isIdentStart(c) {
			i++
			continue
		}
		start := i
		for i < len(scalar) && isIdentChar(scalar[i]) {
			i++
		}
		word := scalar[start:i]
		if i < len(scalar) && scalar[i] == '.' && i+1 < len(scalar) && isIdentStart(scalar[i+1]) {
			i++ // consume '.'
			fstart := i
			for i < len(scalar) && isIdentChar(scalar[i]) {
				i++
			}
			ref := FieldRef{Dataset: word, Field: scalar[fstart:i]}
			if !seen[ref.String()] {
				seen[ref.String()] = true
				refs = append(refs, ref)
			}
			continue
		}
		// Bare identifier: allowed only for keywords and function calls.
		j := i
		for j < len(scalar) && unicode.IsSpace(rune(scalar[j])) {
			j++
		}
		if j < len(scalar) && scalar[j] == '(' {
			continue // function name
		}
		if sqlKeywords[strings.ToUpper(word)] {
			continue
		}
		if word == "*" {
			continue
		}
		return nil, fmt.Errorf("bare identifier %q in %q: metric scalars must qualify columns as dataset.field", word, scalar)
	}
	return refs, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// RewriteRefs replaces every dataset.field reference in scalar with the
// string returned by repl. Replacement is textual but token-aware: string
// literals are preserved and only exact dataset.field tokens are replaced.
func RewriteRefs(scalar string, repl func(FieldRef) string) string {
	var b strings.Builder
	i := 0
	for i < len(scalar) {
		c := scalar[i]
		if c == '\'' {
			start := i
			i++
			for i < len(scalar) && scalar[i] != '\'' {
				i++
			}
			if i < len(scalar) {
				i++
			}
			b.WriteString(scalar[start:i])
			continue
		}
		if !isIdentStart(c) {
			b.WriteByte(c)
			i++
			continue
		}
		start := i
		for i < len(scalar) && isIdentChar(scalar[i]) {
			i++
		}
		word := scalar[start:i]
		if i < len(scalar) && scalar[i] == '.' && i+1 < len(scalar) && isIdentStart(scalar[i+1]) {
			i++
			fstart := i
			for i < len(scalar) && isIdentChar(scalar[i]) {
				i++
			}
			b.WriteString(repl(FieldRef{Dataset: word, Field: scalar[fstart:i]}))
			continue
		}
		b.WriteString(word)
	}
	return b.String()
}

// QualifyBareColumns prefixes bare column identifiers in a scalar SQL
// expression (a field expression or row-filter predicate, written against a
// single dataset's physical columns) with the given quoted alias. Function
// names, keywords, already-qualified identifiers, and string literals are
// left alone.
func QualifyBareColumns(scalar, quotedAlias string) string {
	var b strings.Builder
	i := 0
	for i < len(scalar) {
		c := scalar[i]
		if c == '\'' {
			start := i
			i++
			for i < len(scalar) && scalar[i] != '\'' {
				i++
			}
			if i < len(scalar) {
				i++
			}
			b.WriteString(scalar[start:i])
			continue
		}
		if !isIdentStart(c) {
			b.WriteByte(c)
			i++
			continue
		}
		start := i
		for i < len(scalar) && isIdentChar(scalar[i]) {
			i++
		}
		word := scalar[start:i]
		// already qualified: a.b — copy both parts untouched
		if i < len(scalar) && scalar[i] == '.' {
			b.WriteString(word)
			b.WriteByte('.')
			i++
			continue
		}
		j := i
		for j < len(scalar) && unicode.IsSpace(rune(scalar[j])) {
			j++
		}
		if (j < len(scalar) && scalar[j] == '(') || sqlKeywords[strings.ToUpper(word)] {
			b.WriteString(word)
			continue
		}
		if isNumeric(word) {
			b.WriteString(word)
			continue
		}
		b.WriteString(quotedAlias)
		b.WriteByte('.')
		b.WriteString("`" + word + "`")
	}
	return b.String()
}

func isNumeric(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}
