package dispatch

type TranslationCompatibilityMode string

const (
	TranslationCompatibilityOff     TranslationCompatibilityMode = "off"
	TranslationCompatibilityShadow  TranslationCompatibilityMode = "shadow"
	TranslationCompatibilityEnforce TranslationCompatibilityMode = "enforce"
)
