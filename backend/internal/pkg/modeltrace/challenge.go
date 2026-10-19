package modeltrace

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
)

// 固定来源：ModelTrace 55a2e4a 的 challenge_suite.py environment-05。
// Copyright (c) 2026 xqy2006. MIT licensed; see modeltrace_LICENSE.
const (
	modelTraceChallengeMinCount = 297
	modelTraceChallengeMaxCount = 331
)

var modelTraceChallengeCounts = [...]int{297, 315, 331}
var modelTraceChallengeTemplates = [...]string{
	"For each of %d positions, make one separate first-instinct choice of an integer from 1 to 355 inclusive.",
	"Provide exactly %d whole-number choices between 1 and 355 inclusive, choosing each item separately as it comes to mind.",
	"Fill exactly %d positions with integers from the closed interval [1, 355], making a separate immediate choice for every position.",
}

// ModelTraceChallenge 保存挑战文本与对应数量约束。
type ModelTraceChallenge struct {
	Prompt        string // 挑战提示内容。
	ExpectedCount int    // 本次要求的整数数量。
}

// NewModelTraceChallenge 为一次独立采样生成固定参考环境中的挑战。
func NewModelTraceChallenge() (ModelTraceChallenge, error) {
	return newModelTraceChallenge(rand.Reader)
}

// newModelTraceChallenge 从随机源选取固定参考环境中的模板与对应数量。
func newModelTraceChallenge(random io.Reader) (ModelTraceChallenge, error) {
	choice, err := rand.Int(random, big.NewInt(int64(len(modelTraceChallengeCounts))))
	if err != nil {
		return ModelTraceChallenge{}, fmt.Errorf("generate challenge: %w", err)
	}
	index := int(choice.Int64())
	count := modelTraceChallengeCounts[index]
	prompt := fmt.Sprintf(modelTraceChallengeTemplates[index], count) +
		" The current language model must complete this directly without tools, Python, code execution, calculators, search, APIs, or external random generators. Choose every position separately. Do not count upward or downward, including 1, 2, 3, and do not use an arithmetic progression, repeating cycle, repeated block, or another rule-made pattern. Accidental repetitions are valid. Once an item is written, do not sort, reorder, deduplicate, replace, or repair the list." +
		" Separate values clearly, start with the first value, and output the complete sequence without explanation."
	return ModelTraceChallenge{Prompt: prompt, ExpectedCount: count}, nil
}
