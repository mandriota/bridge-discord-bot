package main

import "strings"

func optionToTypeOrZero[T any](p *T) (v T) {
	if p == nil {
		return
	}

	return *p
}

func skipLine(s string) (i int) {
	el := rune(0)

	for i, el = range s {
		if el == '\n' {
			return i + 1
		}
	}

	return len(s)
}

func skipPrefixedLine(s, prefix string) int {
	i := 0

	for strings.HasPrefix(s[i:], prefix) {
		i = skipLine(s)
	}

	return i
}

func nthRune(s string, n int) (i int) {
	runeCount := 0
	
	for i = range s {
		if runeCount == n {
			return i
		}
		runeCount++
	}

	return i
}
