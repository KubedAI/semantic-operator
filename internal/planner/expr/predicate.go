package expr

import (
	"fmt"
	"sort"
	"strings"
)

// ParsePredicate parses a governance row-filter predicate against a small,
// bounded boolean grammar. Row-filter predicates come from the SemanticModel
// and are injected into the WHERE clause of governed queries, so bounding them
// keeps a filter from smuggling arbitrary SQL (subqueries, function calls,
// statement terminators, comments) into the control plane. It returns the bare
// column names the predicate references, or an error when the predicate is
// outside the grammar.
//
// Grammar (columns are the dataset's physical columns, bare identifiers):
//
//	pred       := or
//	or         := and ( 'OR' and )*
//	and        := not ( 'AND' not )*
//	not        := 'NOT'? primary
//	primary    := '(' or ')' | comparison
//	comparison := col cmp literal
//	            | col ('IN' | 'NOT' 'IN') '(' literal (',' literal)* ')'
//	            | col 'IS' 'NOT'? 'NULL'
//	            | col ('LIKE' | 'NOT' 'LIKE') string
//	cmp        := '=' | '!=' | '<>' | '<' | '<=' | '>' | '>='
//	literal    := string | number | 'TRUE' | 'FALSE' | 'NULL'
func ParsePredicate(s string) ([]string, error) {
	toks, err := tokenizePredicate(s)
	if err != nil {
		return nil, err
	}
	p := &predParser{toks: toks, cols: map[string]bool{}}
	if err := p.parseOr(); err != nil {
		return nil, err
	}
	if p.peek().kind != "eof" {
		return nil, fmt.Errorf("unexpected trailing input %q", p.peek().val)
	}
	if len(p.cols) == 0 {
		return nil, fmt.Errorf("predicate references no column")
	}
	cols := make([]string, 0, len(p.cols))
	for c := range p.cols {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	return cols, nil
}

type ptok struct {
	kind string // ident string number op ( ) , eof
	val  string
}

func tokenizePredicate(s string) ([]ptok, error) {
	var toks []ptok
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == ';':
			return nil, fmt.Errorf("';' is not allowed in a row-filter predicate")
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			return nil, fmt.Errorf("SQL comment '--' is not allowed")
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			return nil, fmt.Errorf("SQL comment '/*' is not allowed")
		case c == '(':
			toks = append(toks, ptok{"(", "("})
			i++
		case c == ')':
			toks = append(toks, ptok{")", ")"})
			i++
		case c == ',':
			toks = append(toks, ptok{",", ","})
			i++
		case c == '\'':
			j := i + 1
			var sb strings.Builder
			for j < len(s) {
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' { // '' escapes a quote
						sb.WriteByte('\'')
						j += 2
						continue
					}
					break
				}
				sb.WriteByte(s[j])
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated string literal")
			}
			toks = append(toks, ptok{"string", sb.String()})
			i = j + 1
		case c == '=':
			toks = append(toks, ptok{"op", "="})
			i++
		case c == '!':
			if i+1 < len(s) && s[i+1] == '=' {
				toks = append(toks, ptok{"op", "!="})
				i += 2
			} else {
				return nil, fmt.Errorf("unexpected '!'")
			}
		case c == '<':
			switch {
			case i+1 < len(s) && s[i+1] == '=':
				toks = append(toks, ptok{"op", "<="})
				i += 2
			case i+1 < len(s) && s[i+1] == '>':
				toks = append(toks, ptok{"op", "<>"})
				i += 2
			default:
				toks = append(toks, ptok{"op", "<"})
				i++
			}
		case c == '>':
			if i+1 < len(s) && s[i+1] == '=' {
				toks = append(toks, ptok{"op", ">="})
				i += 2
			} else {
				toks = append(toks, ptok{"op", ">"})
				i++
			}
		case c >= '0' && c <= '9' || (c == '-' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9'):
			j, err := scanNumber(s, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, ptok{"number", s[i:j]})
			i = j
		case isIdentStart(c):
			j := i
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			toks = append(toks, ptok{"ident", s[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q in predicate", string(c))
		}
	}
	return append(toks, ptok{"eof", ""}), nil
}

func scanNumber(s string, i int) (int, error) {
	j := i
	if s[j] == '-' {
		j++
	}
	startDigits := j
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if startDigits == j {
		return 0, fmt.Errorf("invalid number starting at %q", s[i:])
	}
	if j < len(s) && s[j] == '.' {
		j++
		fracStart := j
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if fracStart == j {
			return 0, fmt.Errorf("invalid number %q", s[i:j])
		}
	}
	if j < len(s) && s[j] == '.' {
		return 0, fmt.Errorf("invalid number %q", s[i:])
	}
	return j, nil
}

type predParser struct {
	toks []ptok
	pos  int
	cols map[string]bool
}

func (p *predParser) peek() ptok { return p.toks[p.pos] }
func (p *predParser) next() ptok { t := p.toks[p.pos]; p.pos++; return t }

func (p *predParser) isKw(t ptok, kw string) bool {
	return t.kind == "ident" && strings.EqualFold(t.val, kw)
}

func (p *predParser) parseOr() error {
	if err := p.parseAnd(); err != nil {
		return err
	}
	for p.isKw(p.peek(), "OR") {
		p.next()
		if err := p.parseAnd(); err != nil {
			return err
		}
	}
	return nil
}

func (p *predParser) parseAnd() error {
	if err := p.parseNot(); err != nil {
		return err
	}
	for p.isKw(p.peek(), "AND") {
		p.next()
		if err := p.parseNot(); err != nil {
			return err
		}
	}
	return nil
}

func (p *predParser) parseNot() error {
	if p.isKw(p.peek(), "NOT") {
		p.next()
	}
	return p.parsePrimary()
}

func (p *predParser) parsePrimary() error {
	if p.peek().kind == "(" {
		p.next()
		if err := p.parseOr(); err != nil {
			return err
		}
		if p.peek().kind != ")" {
			return fmt.Errorf("expected ')'")
		}
		p.next()
		return nil
	}
	return p.parseComparison()
}

func (p *predParser) parseComparison() error {
	t := p.peek()
	if t.kind != "ident" || sqlKeywords[strings.ToUpper(t.val)] {
		return fmt.Errorf("expected a column name, got %q", t.val)
	}
	col := p.next()
	if p.peek().kind == "(" {
		return fmt.Errorf("function calls are not allowed (%q)", col.val)
	}
	p.cols[col.val] = true

	nt := p.peek()
	switch {
	case nt.kind == "op":
		p.next()
		return p.parseLiteral()
	case p.isKw(nt, "IN"):
		p.next()
		return p.parseInList()
	case p.isKw(nt, "LIKE"):
		p.next()
		return p.parseStringLiteral()
	case p.isKw(nt, "IS"):
		p.next()
		if p.isKw(p.peek(), "NOT") {
			p.next()
		}
		if !p.isKw(p.peek(), "NULL") {
			return fmt.Errorf("expected NULL after IS")
		}
		p.next()
		return nil
	case p.isKw(nt, "NOT"):
		p.next()
		if p.isKw(p.peek(), "IN") {
			p.next()
			return p.parseInList()
		}
		if p.isKw(p.peek(), "LIKE") {
			p.next()
			return p.parseStringLiteral()
		}
		return fmt.Errorf("expected IN or LIKE after NOT")
	default:
		return fmt.Errorf("expected a comparison operator after column %q", col.val)
	}
}

func (p *predParser) parseInList() error {
	if p.peek().kind != "(" {
		return fmt.Errorf("expected '(' after IN")
	}
	p.next()
	if err := p.parseLiteral(); err != nil {
		return err
	}
	for p.peek().kind == "," {
		p.next()
		if err := p.parseLiteral(); err != nil {
			return err
		}
	}
	if p.peek().kind != ")" {
		return fmt.Errorf("expected ')' to close IN list")
	}
	p.next()
	return nil
}

func (p *predParser) parseLiteral() error {
	t := p.peek()
	switch {
	case t.kind == "string" || t.kind == "number":
		p.next()
		return nil
	case p.isKw(t, "TRUE") || p.isKw(t, "FALSE") || p.isKw(t, "NULL"):
		p.next()
		return nil
	default:
		return fmt.Errorf("expected a literal (string, number, TRUE, FALSE, NULL), got %q", t.val)
	}
}

func (p *predParser) parseStringLiteral() error {
	if p.peek().kind != "string" {
		return fmt.Errorf("expected a string literal after LIKE")
	}
	p.next()
	return nil
}
