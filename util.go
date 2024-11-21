package main

func optionToTypeOrZero[T any](p *T) (v T) {
	if p == nil {
		return
	}

	return *p
}
