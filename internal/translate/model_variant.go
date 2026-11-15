package translate

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const contextWindowVariantTag = "[1m]"

func CanonicalModel(model string) (canonical string, hadVariantTag bool) {
	if strings.HasSuffix(model, contextWindowVariantTag) {
		return model[:len(model)-len(contextWindowVariantTag)], true
	}
	return model, false
}

func StripProviderPrefix(model, provider string) (stripped string, hadPrefix bool) {
	prefix := provider + "/"
	if provider != "" && strings.HasPrefix(model, prefix) {
		return model[len(prefix):], true
	}
	return model, false
}

func StripProviderPrefixInBody(body []byte, provider string) (out []byte, hadPrefix bool, err error) {
	stripped, had := StripProviderPrefix(gjson.GetBytes(body, "model").String(), provider)
	if !had {
		return body, false, nil
	}
	out, err = sjson.SetBytes(body, "model", stripped)
	if err != nil {
		return body, true, err
	}
	return out, true, nil
}

func CanonicalizeModelInBody(body []byte) (out []byte, hadVariantTag bool, err error) {
	canonical, had := CanonicalModel(gjson.GetBytes(body, "model").String())
	if !had {
		return body, false, nil
	}
	out, err = sjson.SetBytes(body, "model", canonical)
	if err != nil {
		return body, true, err
	}
	return out, true, nil
}
