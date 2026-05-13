package metadata

import "strings"

// CalculateConfidence scores `candidate` against `query` (text) and an
// optional `original` (existing ebook metadata for identifier comparison).
// Weights: ISBN 35, title 30, author 20, completeness 10, ASIN 5.
// Missing inputs are skipped, not penalized.
func CalculateConfidence(query string, candidate Candidate, original *Candidate) int {
	score := 0

	if candidate.Title != "" && query != "" {
		score += titleScore(query, candidate.Title)
	}

	if original != nil && original.ISBN != "" && candidate.ISBN != "" {
		if isbnsMatch(original.ISBN, candidate.ISBN) {
			score += 35
		}
	}

	if original != nil && len(original.Authors) > 0 && len(candidate.Authors) > 0 {
		score += int(float64(matchingNames(original.Authors, candidate.Authors)) /
			float64(len(original.Authors)) * 20.0)
	}

	if original != nil && original.ASIN != "" && candidate.ASIN != "" {
		if strings.EqualFold(original.ASIN, candidate.ASIN) {
			score += 5
		}
	}

	score += completenessScore(candidate)

	if score > 100 {
		return 100
	}
	return score
}

// titleScore: 30 for case-insensitive equality, 22 for substring containment,
// otherwise proportional to word overlap.
func titleScore(query, title string) int {
	q := strings.ToLower(strings.TrimSpace(query))
	t := strings.ToLower(strings.TrimSpace(title))
	if q == t {
		return 30
	}
	if strings.Contains(t, q) || strings.Contains(q, t) {
		return 22
	}
	qw := strings.Fields(q)
	tw := strings.Fields(t)
	if len(qw) == 0 {
		return 0
	}
	matched := 0
	for _, w := range qw {
		for _, tword := range tw {
			if strings.Contains(tword, w) || strings.Contains(w, tword) {
				matched++
				break
			}
		}
	}
	return int(float64(matched) / float64(len(qw)) * 30.0)
}

// matchingNames returns the count of names from `original` that match any
// name in `candidates` (case-insensitive substring either direction).
func matchingNames(original, candidates []string) int {
	n := 0
	for _, o := range original {
		ol := strings.ToLower(strings.TrimSpace(o))
		if ol == "" {
			continue
		}
		for _, c := range candidates {
			cl := strings.ToLower(strings.TrimSpace(c))
			if cl == "" {
				continue
			}
			if strings.Contains(cl, ol) || strings.Contains(ol, cl) {
				n++
				break
			}
		}
	}
	return n
}

// completenessScore awards up to 10 points based on field population.
// 9 fields counted: title, authors, description, cover_url, (asin OR isbn),
// publisher, language, page_count, genres.
func completenessScore(c Candidate) int {
	fields := 0
	if c.Title != "" {
		fields++
	}
	if len(c.Authors) > 0 {
		fields++
	}
	if c.Description != "" {
		fields++
	}
	if c.CoverURL != "" {
		fields++
	}
	if c.ASIN != "" || c.ISBN != "" {
		fields++
	}
	if c.Publisher != "" {
		fields++
	}
	if c.Language != "" {
		fields++
	}
	if c.PageCount > 0 {
		fields++
	}
	if len(c.Genres) > 0 {
		fields++
	}
	return int(float64(fields) / 9.0 * 10.0)
}

// isbnsMatch returns true if two ISBN strings represent the same book.
// Normalizes by stripping non-digit (except trailing X) chars and supports
// ISBN-10 ↔ ISBN-13 cross-format match via the 9-digit core (ISBN-13 has
// "978" or "979" prefix + 9-digit core + check digit; ISBN-10 has 9-digit
// core + check digit).
func isbnsMatch(a, b string) bool {
	an := normalizeISBN(a)
	bn := normalizeISBN(b)
	if an == "" || bn == "" {
		return false
	}
	if an == bn {
		return true
	}
	if len(an) == 10 && len(bn) == 13 && strings.HasPrefix(bn, "978") {
		return an[:9] == bn[3:12]
	}
	if len(bn) == 10 && len(an) == 13 && strings.HasPrefix(an, "978") {
		return bn[:9] == an[3:12]
	}
	return false
}

func normalizeISBN(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || c == 'X' || c == 'x' {
			if c == 'x' {
				c = 'X'
			}
			out = append(out, c)
		}
	}
	return string(out)
}
