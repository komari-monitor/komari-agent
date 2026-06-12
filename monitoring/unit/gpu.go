package monitoring

import (
	"fmt"
	"strings"
)

func formatGPUNameList(names []string) string {
	counts := make(map[string]int)
	order := make([]string, 0, len(names))

	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
	}

	if len(order) == 0 {
		return "None"
	}

	result := make([]string, 0, len(order))
	for _, name := range order {
		if counts[name] > 1 {
			result = append(result, fmt.Sprintf("%s × %d", name, counts[name]))
			continue
		}
		result = append(result, name)
	}

	return strings.Join(result, ", ")
}
