package catalog

type ImageInput int

const (
	ImageInputUnknown ImageInput = iota
	ImageInputUnsupported
)

type CustomToolInput int

const (
	CustomToolInputUnknown CustomToolInput = iota
	CustomToolInputUnsupported
)
