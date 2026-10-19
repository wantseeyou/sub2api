package modeltrace

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
)

const AlgorithmVersion = "modeltrace-ordered-hellinger-v1-strict1"
const ChallengeVersion = "modeltrace-clean-environment05-55a2e4a"

// Validate 排除数量不符、范围错误、明显等差或循环输出，避免将无效挑战强行分类。
func Validate(text string, expectedCount int) ([]int, error) {
	if expectedCount < modelTraceChallengeMinCount || expectedCount > modelTraceChallengeMaxCount || len(text) > 32<<10 {
		return nil, errors.New("invalid_sample")
	}
	valid := true
	parts := strings.FieldsFunc(text, func(r rune) bool {
		if r >= '0' && r <= '9' {
			return false
		}
		if !unicode.IsSpace(r) && !strings.ContainsRune(",;，、；", r) {
			valid = false
		}
		return true
	})
	if !valid || len(parts) != expectedCount {
		return nil, errors.New("invalid_sample")
	}
	numbers := make([]int, len(parts))
	distinct := make(map[int]struct{})
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 1 || value > modelTraceDimension {
			return nil, errors.New("invalid_sample")
		}
		numbers[i] = value
		distinct[value] = struct{}{}
	}
	if len(distinct) < 16 {
		return nil, errors.New("invalid_sample")
	}
	consecutive := 1
	for i := 2; i < len(numbers); i++ {
		if numbers[i]-numbers[i-1] == numbers[i-1]-numbers[i-2] {
			consecutive++
		} else {
			consecutive = 1
		}
		if consecutive >= 12 {
			return nil, errors.New("invalid_sample")
		}
	}
	for period := 1; period <= len(numbers)/3; period++ {
		periodic := true
		for i := period; i < len(numbers); i++ {
			if numbers[i] != numbers[i%period] {
				periodic = false
				break
			}
		}
		if periodic {
			return nil, errors.New("invalid_sample")
		}
	}
	return numbers, nil
}
