package constant

// TransparentCredentialSpec describes credential replacement for mount-prefix
// HTTP channels. Custom full-endpoint templates are deliberately unsupported.
type TransparentCredentialSpec struct {
	Header string
	Prefix string
}

func GetTransparentCredentialSpec(channelType int) (TransparentCredentialSpec, bool) {
	switch channelType {
	case ChannelTypeAnthropic:
		return TransparentCredentialSpec{Header: "X-Api-Key"}, true
	case ChannelTypeGemini:
		return TransparentCredentialSpec{Header: "X-Goog-Api-Key"}, true
	case ChannelTypeAzure:
		return TransparentCredentialSpec{Header: "Api-Key"}, true
	case ChannelTypeOpenAI, ChannelTypeOpenRouter, ChannelTypeNewAPI,
		ChannelTypeDeepSeek, ChannelTypeMistral, ChannelTypeXai, ChannelTypeMoonshot,
		ChannelTypeSiliconFlow, ChannelTypeVolcEngine, ChannelTypeJina, ChannelTypeCohere:
		return TransparentCredentialSpec{Header: "Authorization", Prefix: "Bearer "}, true
	default:
		return TransparentCredentialSpec{}, false
	}
}
