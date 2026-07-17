package config

import (
	"strings"

	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/spf13/viper"
)

// Config represents the whole settings.toml
type Config struct {
	BaseURL      string          `mapstructure:"base_url"`
	MaxRetries   int             `mapstructure:"max_retries"`
	DefaultModel string          `mapstructure:"default_model"`
	Models       []ModelSettings `mapstructure:"models"`
}

// ModelSettings represents one [models.'<name>'] block
type ModelSettings struct {
	Name             string  `mapstructure:"name"`
	CtxSize          int     `mapstructure:"context_size"`
	Temperature      float32 `mapstructure:"temperature"`
	TopP             float32 `mapstructure:"top_p"`
	TopK             int     `mapstructure:"top_k"`
	MinP             float32 `mapstructure:"min_p"`
	RepeatPenalty    float32 `mapstructure:"repeat_penalty"`
	PresencePenalty  float32 `mapstructure:"presence_penalty"`
	FrequencyPenalty float32 `mapstructure:"frequency_penalty"`
}

var (
	Logger = logger.Logger
	cfg    Config
)

func Load() error {
	return viper.Unmarshal(&cfg)
}

func GetConfig() Config {
	return cfg
}

func DefaultModel() ModelSettings {
	for _, modelSettings := range cfg.Models {
		if strings.EqualFold(modelSettings.Name, cfg.DefaultModel) {
			return modelSettings
		}
	}
	Logger.Error("no settings found for default_model", "default_model", cfg.DefaultModel)
	return ModelSettings{}
}
