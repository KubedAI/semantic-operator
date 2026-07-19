package expr

import "testing"

func TestParsePredicateValid(t *testing.T) {
	cases := []struct {
		in   string
		cols []string
	}{
		{"s_state = 'TX'", []string{"s_state"}},
		{"d_year >= 2000", []string{"d_year"}},
		{"s_state IN ('TX', 'CA', 'NY')", []string{"s_state"}},
		{"region = 'US' AND active = TRUE", []string{"active", "region"}},
		{"(a = 1 OR b = 2) AND c != 'x'", []string{"a", "b", "c"}},
		{"tier NOT IN ('free', 'trial')", []string{"tier"}},
		{"email LIKE '%@corp.com'", []string{"email"}},
		{"deleted_at IS NULL", []string{"deleted_at"}},
		{"owner IS NOT NULL AND price <= 99.5", []string{"owner", "price"}},
		{"NOT (status = 'archived')", []string{"status"}},
	}
	for _, c := range cases {
		cols, err := ParsePredicate(c.in)
		if err != nil {
			t.Errorf("ParsePredicate(%q) unexpected error: %v", c.in, err)
			continue
		}
		if len(cols) != len(c.cols) {
			t.Errorf("ParsePredicate(%q) cols = %v, want %v", c.in, cols, c.cols)
			continue
		}
		for i := range cols {
			if cols[i] != c.cols[i] {
				t.Errorf("ParsePredicate(%q) cols = %v, want %v", c.in, cols, c.cols)
				break
			}
		}
	}
}

func TestParsePredicateRejectsUnsafe(t *testing.T) {
	bad := []string{
		"s_state = 'TX'; DROP TABLE store",        // statement terminator
		"s_state IN (SELECT s FROM secret)",       // subquery
		"lower(s_state) = 'tx'",                   // function call
		"s_state = 'TX' OR 1 = 1",                 // literal on the left (tautology shape)
		"s_state = 'TX' -- comment",               // sql comment
		"s_state = 'TX' /* comment */",            // block comment
		"s_state",                                 // no comparison
		"s_state = ",                              // missing literal
		"= 'TX'",                                  // missing column
		"s_state == 'TX'",                         // invalid operator
		"s_state = 'unterminated",                 // unterminated string
		"s_state & 1",                             // stray operator char
		"price = 1.2.3",                           // malformed numeric literal
		"price = 1.",                              // trailing decimal point
		"",                                        // empty
	}
	for _, in := range bad {
		if _, err := ParsePredicate(in); err == nil {
			t.Errorf("ParsePredicate(%q) = nil error, want rejection", in)
		}
	}
}
