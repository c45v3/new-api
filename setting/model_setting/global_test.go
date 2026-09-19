package model_setting

import (
	"bytes"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGlobalSettingsIsRequestBodyPassthroughEnabled(t *testing.T) {
	for _, tt := range []struct {
		name      string
		enabled   bool
		excluded  []int
		channelID int
		want      bool
	}{
		{name: "disabled", channelID: 1},
		{name: "enabled", enabled: true, channelID: 1, want: true},
		{name: "excluded", enabled: true, excluded: []int{1}, channelID: 1},
		{name: "other channel", enabled: true, excluded: []int{1}, channelID: 2, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			settings := &GlobalSettings{
				PassThroughRequestEnabled:          tt.enabled,
				PassThroughRequestExcludedChannels: tt.excluded,
			}
			assert.Equal(t, tt.want, settings.IsRequestBodyPassthroughEnabled(tt.channelID))
		})
	}
}

func TestValidatePassThroughRequestExcludedChannels(t *testing.T) {
	for _, value := range []string{"[]", "[1]", "[1, 2]"} {
		t.Run("accepts "+value, func(t *testing.T) {
			require.NoError(t, ValidatePassThroughRequestExcludedChannels(value))
		})
	}

	for _, value := range []string{"", "null", "{}", "[0]", "[-1]", "[1.5]", `["1"]`} {
		t.Run("rejects "+value, func(t *testing.T) {
			assert.Error(t, ValidatePassThroughRequestExcludedChannels(value))
		})
	}
}

func TestShouldPreserveThinkingSuffixExactAndRegex(t *testing.T) {
	settings := GetGlobalSettings()
	original := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = original })

	assert.True(t, ShouldPreserveThinkingSuffix("kimi-k2-thinking"))
	assert.True(t, ShouldPreserveThinkingSuffix("moonshotai/kimi-k2-thinking"))
	assert.False(t, ShouldPreserveThinkingSuffix("m@sha256:abc"))

	settings.ThinkingModelBlacklist = []string{
		"kimi-k2-thinking",
		"re:[",
		"re:",
		"re:.*@sha256:.*",
	}

	var logged bytes.Buffer
	previous := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logged
	t.Cleanup(func() { gin.DefaultErrorWriter = previous })

	assert.True(t, ShouldPreserveThinkingSuffix("kimi-k2-thinking"))
	assert.True(t, ShouldPreserveThinkingSuffix("m@sha256:abc"))
	assert.False(t, ShouldPreserveThinkingSuffix("m@sha256"))
	assert.False(t, ShouldPreserveThinkingSuffix("qwen3-max@thinking:on"))
	require.Contains(t, logged.String(), `invalid thinking_model_blacklist regex "re:["`)
	require.Contains(t, logged.String(), `invalid thinking_model_blacklist regex "re:"`)

	settings.ThinkingModelBlacklist = []string{"re:^beta@"}
	assert.False(t, ShouldPreserveThinkingSuffix("m@sha256:abc"))
	assert.True(t, ShouldPreserveThinkingSuffix("beta@sha256:abc"))
	assert.False(t, ShouldPreserveThinkingSuffix("alpha@sha256:abc"))
}
