package noncore_service

import (
	"fmt"
	"strconv"
	"strings"
)

func getStringMap(m map[string]interface{}, key string, fallback string) string {
	if m == nil {
		return fallback
	}
	if raw, ok := m[key]; ok {
		switch v := raw.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return v
			}
		}
	}
	return fallback
}

func getIntMap(m map[string]interface{}, key string, fallback int) int {
	if m == nil {
		return fallback
	}
	raw, ok := m[key]
	if !ok {
		return fallback
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return i
		}
	}
	return fallback
}

func getStringStringMap(m map[string]interface{}, key string) map[string]string {
	out := make(map[string]string)
	if m == nil {
		return out
	}
	raw, ok := m[key]
	if !ok || raw == nil {
		return out
	}
	typed, ok := raw.(map[string]interface{})
	if !ok {
		return out
	}
	for k, v := range typed {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}
