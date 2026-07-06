package config

import (
	"fmt"
	"os"
	"strings"

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

var cfg Config

func Load() {
	if err := viper.Unmarshal(&cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error unmarshaling config:", err)
		os.Exit(1)
	}
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
	fmt.Fprintf(os.Stderr, "no settings found for default_model %q\n", cfg.DefaultModel)
	os.Exit(1)
	return ModelSettings{} // unreachable
}
