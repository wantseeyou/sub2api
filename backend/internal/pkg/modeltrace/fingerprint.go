package modeltrace

import (
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"sync"
)

const modelTraceFallbackCommit = "55a2e4a55170423b484d701e9a82ab62b268c811"
const modelTraceDimension = 355

//go:embed modeltrace_unified_bank.json
var embeddedModelTraceBank []byte

type modelTraceModel struct {
	ID     string `json:"id"`     // 模型名称
	Family string `json:"family"` // 模型家族
}

type modelTraceArtifact struct {
	Weight               float64       `json:"weight"`                // 特征融合权重
	FeatureMean          []float64     `json:"feature_mean"`          // 特征均值
	FeatureScale         []float64     `json:"feature_scale"`         // 特征缩放系数
	NuisanceBasis        [][]float64   `json:"nuisance_basis"`        // 环境偏差基向量
	Centroids            [][]float64   `json:"centroids"`             // 模型特征中心
	EnvironmentCentroids [][][]float64 `json:"environment_centroids"` // 各环境模型中心
}

type modelTraceBank struct {
	Schema string            `json:"schema"` // 指纹库格式
	Models []modelTraceModel `json:"models"` // 参考模型
	Robust struct {          // 统计特征参数
		ModelOrder    []string           `json:"model_order"`    // 特征中心对应的模型顺序
		Hellinger     modelTraceArtifact `json:"hellinger"`      // 边际分布特征
		OrderedBlocks modelTraceArtifact `json:"ordered_blocks"` // 顺序分块特征
	} `json:"robust"`
	Calibration map[string]struct { // 固定分数标定参数
		Beta float64 `json:"beta"` // 温度缩放参数
	} `json:"calibration"`
}

type ModelTracePrediction struct {
	Model       string  // 候选模型
	Probability float64 // 闭集相对分数，不是准确率
	ParsedCount int     // 解析出的整数数量
}

type modelTraceVersion struct {
	bank   *modelTraceBank // 固定指纹库
	commit string          // 指纹资源来源版本
}

var modelTraceFallback = sync.OnceValues(func() (*modelTraceBank, error) {
	return parseModelTraceBank(embeddedModelTraceBank)
})

// parseModelTraceBank 解析固定指纹库并校验模型顺序及特征维数。
func parseModelTraceBank(data []byte) (*modelTraceBank, error) {
	var bank modelTraceBank
	if err := json.Unmarshal(data, &bank); err != nil {
		return nil, err
	}
	if bank.Schema != "robust-number-fingerprint-bank" || len(bank.Models) == 0 || len(bank.Models) != len(bank.Robust.ModelOrder) {
		return nil, errors.New("invalid ModelTrace bank schema")
	}
	for index, model := range bank.Models {
		if model.ID == "" || model.ID != bank.Robust.ModelOrder[index] {
			return nil, errors.New("invalid ModelTrace model order")
		}
	}
	for _, artifact := range []struct {
		data      modelTraceArtifact // 当前验证的特征组。
		dimension int                // 特征组的预期维数。
	}{
		{bank.Robust.Hellinger, modelTraceDimension},
		{bank.Robust.OrderedBlocks, 74},
	} {
		if len(artifact.data.FeatureMean) != artifact.dimension || len(artifact.data.FeatureScale) != artifact.dimension || len(artifact.data.Centroids) != len(bank.Models) {
			return nil, errors.New("invalid ModelTrace feature dimensions")
		}
		for _, scale := range artifact.data.FeatureScale {
			if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
				return nil, errors.New("invalid ModelTrace feature scale")
			}
		}
		for _, centroid := range artifact.data.Centroids {
			if len(centroid) != artifact.dimension {
				return nil, errors.New("invalid ModelTrace centroid")
			}
		}
		for _, basis := range artifact.data.NuisanceBasis {
			if len(basis) != artifact.dimension {
				return nil, errors.New("invalid ModelTrace nuisance basis")
			}
		}
	}
	for _, environment := range bank.Robust.OrderedBlocks.EnvironmentCentroids {
		if len(environment) != len(bank.Models) {
			return nil, errors.New("invalid ModelTrace environment")
		}
		for _, centroid := range environment {
			if len(centroid) != 74 {
				return nil, errors.New("invalid ModelTrace environment centroid")
			}
		}
	}
	if len(bank.Robust.OrderedBlocks.EnvironmentCentroids) == 0 || bank.Calibration["1"].Beta <= 0 {
		return nil, errors.New("missing ModelTrace calibration")
	}
	return &bank, nil
}

// ModelTraceGPTModels 返回固定指纹库中覆盖的 GPT 模型。
func ModelTraceGPTModels() ([]string, error) {
	snapshot, err := currentModelTraceBank()
	if err != nil {
		return nil, err
	}
	bank := snapshot.bank // 固定指纹库
	models := make([]string, 0, len(bank.Models))
	for _, model := range bank.Models {
		if model.Family == "gpt" {
			models = append(models, model.ID)
		}
	}
	return models, nil
}

// modelTraceDot 计算同维向量的内积。
func modelTraceDot(left, right []float64) float64 {
	result := 0.0
	for index, value := range left {
		result += value * right[index]
	}
	return result
}

// modelTraceNormalize 将特征向量归一化为单位长度。
func modelTraceNormalize(values []float64) []float64 {
	length := math.Max(math.Sqrt(modelTraceDot(values, values)), 1e-12)
	output := make([]float64, len(values))
	for index, value := range values {
		output[index] = value / length
	}
	return output
}

// modelTraceStandardize 将候选分数中心化并按标准差缩放。
func modelTraceStandardize(values []float64) []float64 {
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	variance := 0.0
	for _, value := range values {
		variance += (value - mean) * (value - mean)
	}
	scale := math.Max(math.Sqrt(variance/float64(len(values))), 1e-12)
	result := make([]float64, len(values))
	for index, value := range values {
		result[index] = (value - mean) / scale
	}
	return result
}

// modelTraceProject 按指纹库参数缩放样本特征。
func modelTraceProject(feature []float64, artifact modelTraceArtifact) []float64 {
	projected := make([]float64, len(feature))
	for index, value := range feature {
		projected[index] = (value - artifact.FeatureMean[index]) / artifact.FeatureScale[index]
	}
	return projected
}

// modelTraceSubtractBasis 移除特征中的环境偏差方向。
func modelTraceSubtractBasis(values []float64, basis [][]float64) []float64 {
	result := append([]float64(nil), values...)
	for _, vector := range basis {
		projection := modelTraceDot(result, vector)
		for index := range result {
			result[index] -= projection * vector[index]
		}
	}
	return result
}

// modelTraceCentroidScores 计算特征与各模型中心的相似度。
func modelTraceCentroidScores(feature []float64, centroids [][]float64) []float64 {
	scores := make([]float64, len(centroids))
	for index, centroid := range centroids {
		scores[index] = modelTraceDot(feature, centroid)
	}
	return scores
}

// modelTraceScores 融合数字边际分布与顺序分块特征。
func modelTraceScores(numbers []int, bank *modelTraceBank) []float64 {
	counts := make([]float64, modelTraceDimension)
	for _, number := range numbers {
		counts[number-1]++
	}
	hellinger := make([]float64, modelTraceDimension)
	total := float64(len(numbers)) + 0.5*modelTraceDimension
	for index, count := range counts {
		hellinger[index] = math.Sqrt((count + 0.5) / total)
	}
	marginalArtifact := bank.Robust.Hellinger
	projected := modelTraceNormalize(modelTraceSubtractBasis(modelTraceProject(hellinger, marginalArtifact), marginalArtifact.NuisanceBasis))
	marginal := modelTraceStandardize(modelTraceCentroidScores(projected, marginalArtifact.Centroids))
	orderedArtifact := bank.Robust.OrderedBlocks
	if orderedArtifact.Weight == 0 {
		return marginal
	}
	feature := make([]float64, 0, 74)
	base, remainder, start := len(numbers)/4, len(numbers)%4, 0
	for block := 0; block < 4; block++ {
		size := base
		if block < remainder {
			size++
		}
		bins := make([]float64, 16)
		for index := range bins {
			bins[index] = 0.5
		}
		for _, value := range numbers[start : start+size] {
			index := int(math.Floor(float64(value-1) / 355 * 16))
			if index > 15 {
				index = 15
			}
			bins[index]++
		}
		denominator := float64(size) + 8
		for _, value := range bins {
			feature = append(feature, math.Sqrt(value/denominator))
		}
		start += size
	}
	lastDigits := make([]float64, 10)
	for index := range lastDigits {
		lastDigits[index] = 0.5
	}
	for _, number := range numbers {
		lastDigits[number%10]++
	}
	for _, value := range lastDigits {
		feature = append(feature, math.Sqrt(value/(float64(len(numbers))+5)))
	}
	standardized := modelTraceProject(feature, orderedArtifact)
	unit := modelTraceNormalize(standardized)
	templateRaw := make([]float64, len(bank.Models))
	for modelIndex := range templateRaw {
		templateRaw[modelIndex] = math.Inf(-1)
		for _, environment := range orderedArtifact.EnvironmentCentroids {
			templateRaw[modelIndex] = math.Max(templateRaw[modelIndex], modelTraceDot(unit, environment[modelIndex]))
		}
	}
	template := modelTraceStandardize(templateRaw)
	nuisanceFeature := modelTraceNormalize(modelTraceSubtractBasis(standardized, orderedArtifact.NuisanceBasis))
	nuisance := modelTraceStandardize(modelTraceCentroidScores(nuisanceFeature, orderedArtifact.Centroids))
	fused := make([]float64, len(bank.Models))
	for index := range fused {
		fused[index] = 0.5*template[index] + 0.5*nuisance[index]
	}
	ordered := modelTraceStandardize(fused)
	for index := range marginal {
		marginal[index] = (1-orderedArtifact.Weight)*marginal[index] + orderedArtifact.Weight*ordered[index]
	}
	return marginal
}

// ModelTracePredict 校验完整样本后返回最相似的参考候选。
func ModelTracePredict(text string, expectedCount int) (ModelTracePrediction, error) {
	prediction, _, err := ModelTracePredictCommitted(text, expectedCount)
	return prediction, err
}

// ModelTracePredictCommitted 返回候选及所用指纹库版本。
func ModelTracePredictCommitted(text string, expectedCount int) (ModelTracePrediction, string, error) {
	snapshot, err := currentModelTraceBank()
	if err != nil {
		return ModelTracePrediction{}, "", err
	}
	bank := snapshot.bank // 固定指纹库
	numbers, validationErr := Validate(text, expectedCount)
	if validationErr != nil {
		return ModelTracePrediction{}, snapshot.commit, validationErr
	}
	minimum := 80
	if expectedCount > 0 {
		minimum = max(minimum, int(math.Ceil(float64(expectedCount)*0.55)))
	}
	if len(numbers) < minimum {
		return ModelTracePrediction{ParsedCount: len(numbers)}, snapshot.commit, errors.New("insufficient_numbers")
	}
	scores := modelTraceScores(numbers, bank)
	beta := bank.Calibration["1"].Beta
	maximum := math.Inf(-1)
	for _, value := range scores {
		maximum = math.Max(maximum, beta*value)
	}
	weights := make([]float64, len(scores))
	total := 0.0
	winner := 0
	for index, score := range scores {
		weights[index] = math.Exp(beta*score - maximum)
		total += weights[index]
		if score > scores[winner] {
			winner = index
		}
	}
	return ModelTracePrediction{Model: bank.Models[winner].ID, Probability: weights[winner] / total, ParsedCount: len(numbers)}, snapshot.commit, nil
}

// currentModelTraceBank 返回进程内只解析一次的固定指纹库。
func currentModelTraceBank() (*modelTraceVersion, error) {
	bank, err := modelTraceFallback()
	if err != nil {
		return nil, err
	}
	return &modelTraceVersion{bank: bank, commit: modelTraceFallbackCommit}, nil
}

// ModelTraceBankCommit 返回内嵌指纹库来源提交。
func ModelTraceBankCommit() string {
	snapshot, err := currentModelTraceBank()
	if err != nil {
		return ""
	}
	return snapshot.commit
}
