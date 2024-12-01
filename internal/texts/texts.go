package texts

import "strings"

func SkipLine(s string) (i int) {
	el := rune(0)

	for i, el = range s {
		if el == '\n' {
			return i + 1
		}
	}

	return len(s)
}

func SkipPrefixedLine(s, prefix string) int {
	i := 0

	for strings.HasPrefix(s[i:], prefix) {
		i += SkipLine(s[i:])
	}

	return i
}

func NthRune(s string, n int) int {
	runeCount := 0
	
	for i := range s {
		if runeCount == n {
			return i
		}
		runeCount++
	}

	return len(s)
}
