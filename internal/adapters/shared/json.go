package shared

func DigMap(v any, path ...any) (map[string]any, bool) {
	cur := v
	for _, p := range path {
		cur = index(cur, p)
		if cur == nil {
			return nil, false
		}
	}
	m, ok := cur.(map[string]any)
	return m, ok
}

func DigArray(v any, path ...any) []any {
	cur := v
	for _, p := range path {
		cur = index(cur, p)
		if cur == nil {
			return nil
		}
	}
	arr, _ := cur.([]any)
	return arr
}

func DigString(v any, path ...any) (string, bool) {
	cur := v
	for _, p := range path {
		cur = index(cur, p)
		if cur == nil {
			return "", false
		}
	}
	s, ok := cur.(string)
	return s, ok
}

func DigStringFrom(arr []any, idx int, path ...any) (string, bool) {
	if idx < 0 || idx >= len(arr) {
		return "", false
	}
	return DigString(arr[idx], path...)
}

func index(v any, key any) any {
	if v == nil {
		return nil
	}
	switch k := key.(type) {
	case string:
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		return m[k]
	case int:
		arr, ok := v.([]any)
		if !ok || k < 0 || k >= len(arr) {
			return nil
		}
		return arr[k]
	default:
		return nil
	}
}
