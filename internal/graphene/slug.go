package graphene

import (
	"strconv"
	"strings"
)

func SlugSubject(subject string) string {
	line := subject
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}

	var b strings.Builder
	lastDash := false
	for _, r := range line {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			lastDash = false
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}

	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "change"
	}
	return slug
}

func BranchName(prefix, slug string) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return slug
	}
	return prefix + "/" + slug
}

func CandidateName(base string, n int) string {
	if n <= 1 {
		return base
	}
	return base + "-" + strconv.Itoa(n)
}
