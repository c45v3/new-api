package model_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGlobalSettingsIsPassThroughEnabled(t *testing.T) {
	tests := []struct {
		name                  string
		globalEnabled         bool
		excludedChannelIDs    []int
		channelID             int
		channelSettingEnabled bool
		want                  bool
	}{
		{
			name:       "disabled globally and on channel",
			channelID:  1,
			want:       false,
		},
		{
			name:                  "enabled on channel",
			channelID:             1,
			channelSettingEnabled: true,
			want:                  true,
		},
		{
			name:          "enabled globally",
			globalEnabled: true,
			channelID:     1,
			want:          true,
		},
		{
			name:               "excluded channel overrides global setting",
			globalEnabled:      true,
			excludedChannelIDs: []int{1},
			channelID:          1,
			want:               false,
		},
		{
			name:                  "excluded channel overrides channel setting",
			excludedChannelIDs:    []int{1},
			channelID:             1,
			channelSettingEnabled: true,
			want:                  false,
		},
		{
			name:                  "other channels retain their settings",
			excludedChannelIDs:    []int{1},
			channelID:             2,
			channelSettingEnabled: true,
			want:                  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := &GlobalSettings{
				PassThroughRequestEnabled:          tt.globalEnabled,
				PassThroughRequestExcludedChannels: tt.excludedChannelIDs,
			}

			assert.Equal(t, tt.want, settings.IsPassThroughEnabled(tt.channelID, tt.channelSettingEnabled))
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
