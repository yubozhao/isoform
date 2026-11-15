package pii

import (
	"fmt"
	"os"
	"path/filepath"

	"wails-gui/internal/onnxdl"
	"wails-gui/models"
)

const (
	opfModelName    = "openai-privacy-filter"
	opfModelHFRepo  = "openai/privacy-filter"
	opfMirrorBase   = "https://model-assets.yansu.app/openai-privacy-filter"
	envOPFModelURL  = "YANSU_OPF_MODEL_URL"
	envOPFTokenizer = "YANSU_OPF_TOKENIZER_URL"

	opfModelFile     = "model_q4.onnx"
	opfModelDataFile = "model_q4.onnx_data"
	opfTokenizerFile = "tokenizer.json"
)

type opfModelPaths struct {
	ModelFile     string
	ModelDataFile string
	TokenizerFile string
	RuntimeLib    string
}

func ensureOPFModel(configDir string, onProgress ProgressFn) (*opfModelPaths, error) {
	if err := models.Require(configDir, "openai-privacy-filter"); err != nil {
		return nil, err
	}
	modelDir := filepath.Join(configDir, "models", opfModelName)
	if err := os.MkdirAll(modelDir, 0755); err != nil {
		return nil, fmt.Errorf("create model dir: %w", err)
	}

	runtimePaths, err := onnxdl.ResolveRuntimePaths(configDir)
	if err != nil {
		return nil, err
	}

	paths := &opfModelPaths{
		ModelFile:     filepath.Join(modelDir, opfModelFile),
		ModelDataFile: filepath.Join(modelDir, opfModelDataFile),
		TokenizerFile: filepath.Join(modelDir, opfTokenizerFile),
		RuntimeLib:    runtimePaths.RuntimeLib,
	}

	if err := requireBundledRuntime(paths.RuntimeLib); err != nil {
		return nil, err
	}

	return paths, nil
}
