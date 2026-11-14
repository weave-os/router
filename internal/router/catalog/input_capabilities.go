package catalog

// ImageInput marks whether a model accepts image content parts.
// ImageInputUnknown (zero value) = no restriction; first-party models default
// here since they're all multimodal. ImageInputUnsupported flags text-only
// models that 4xx on image parts (e.g. GLM-5.1). The scorer excludes the
// ImageInputUnsupported set from image-bearing requests.
type ImageInput int

const (
	ImageInputUnknown ImageInput = iota
	ImageInputUnsupported
)

// CustomToolInput marks whether a model can call a Responses custom tool
// projected to a Chat function with one required string "input" argument.
// CustomToolInputUnknown (zero value) = no restriction. CustomToolInputUnsupported
// flags models observed returning arguments the projection cannot unwrap;
// the proxy excludes them from requests that declare custom tools.
type CustomToolInput int

const (
	CustomToolInputUnknown CustomToolInput = iota
	CustomToolInputUnsupported
)
