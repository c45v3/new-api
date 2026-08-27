package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateOptionValuePassThroughRequestExcludedChannels(t *testing.T) {
	require.NoError(t, validateOptionValue("global.pass_through_request_excluded_channels", "[1,2]"))
	assert.Error(t, validateOptionValue("global.pass_through_request_excluded_channels", "[0]"))
}
