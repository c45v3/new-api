package model_setting

import (
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

type ChatCompletionsToResponsesPolicy struct {
	Enabled       bool     `json:"enabled"`
	AllChannels   bool     `json:"all_channels"`
	ChannelIDs    []int    `json:"channel_ids,omitempty"`
	ChannelTypes  []int    `json:"channel_types,omitempty"`
	ModelPatterns []string `json:"model_patterns,omitempty"`
}

func (p ChatCompletionsToResponsesPolicy) IsChannelEnabled(channelID int, channelType int) bool {
	if !p.Enabled {
		return false
	}
	if p.AllChannels {
		return true
	}

	if channelID > 0 && len(p.ChannelIDs) > 0 && slices.Contains(p.ChannelIDs, channelID) {
		return true
	}
	if channelType > 0 && len(p.ChannelTypes) > 0 && slices.Contains(p.ChannelTypes, channelType) {
		return true
	}
	return false
}

type GlobalSettings struct {
	PassThroughRequestEnabled          bool                             `json:"pass_through_request_enabled"`
	PassThroughRequestExcludedChannels []int                          `json:"pass_through_request_excluded_channels"`
	ThinkingModelBlacklist             []string                         `json:"thinking_model_blacklist"`
	ChatCompletionsToResponsesPolicy   ChatCompletionsToResponsesPolicy `json:"chat_completions_to_responses_policy"`
}

// 默认配置
var defaultOpenaiSettings = GlobalSettings{
	PassThroughRequestEnabled:          false,
	PassThroughRequestExcludedChannels: []int{},
	ThinkingModelBlacklist: []string{
		"moonshotai/kimi-k2-thinking",
		"kimi-k2-thinking",
	},
	ChatCompletionsToResponsesPolicy: ChatCompletionsToResponsesPolicy{
		Enabled:     false,
		AllChannels: true,
	},
}

// 全局实例
var globalSettings = defaultOpenaiSettings

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("global", &globalSettings)
}

func GetGlobalSettings() *GlobalSettings {
	return &globalSettings
}

// IsPassThroughExcluded reports whether the global pass-through setting is
// explicitly disabled for a channel. Channel IDs are used because channel
// settings are persisted per channel and retries can change channel type.
func (s *GlobalSettings) IsPassThroughExcluded(channelID int) bool {
	if s == nil || channelID <= 0 {
		return false
	}
	return slices.Contains(s.PassThroughRequestExcludedChannels, channelID)
}

// IsPassThroughEnabled resolves the global request pass-through setting for a
// channel. An excluded channel always wins over its own channel setting.
func (s *GlobalSettings) IsPassThroughEnabled(channelID int, channelSettingEnabled bool) bool {
	if s != nil && s.IsPassThroughExcluded(channelID) {
		return false
	}
	return (s != nil && s.PassThroughRequestEnabled) || channelSettingEnabled
}

// ValidatePassThroughRequestExcludedChannels validates the JSON persisted by
// the option API. Each channel ID must be a positive integer.
func ValidatePassThroughRequestExcludedChannels(value string) error {
	var channelIDs []int
	if err := common.UnmarshalJsonStr(value, &channelIDs); err != nil || channelIDs == nil {
		return fmt.Errorf("pass-through request excluded channels must be a JSON array of positive channel IDs")
	}
	for _, channelID := range channelIDs {
		if channelID <= 0 {
			return fmt.Errorf("pass-through request excluded channels must be a JSON array of positive channel IDs")
		}
	}
	return nil
}

// ShouldPreserveThinkingSuffix 判断模型是否配置为保留 thinking/-nothinking/-low/-high/-medium 后缀
func ShouldPreserveThinkingSuffix(modelName string) bool {
	target := strings.TrimSpace(modelName)
	if target == "" {
		return false
	}

	for _, entry := range globalSettings.ThinkingModelBlacklist {
		if strings.TrimSpace(entry) == target {
			return true
		}
	}
	return false
}
