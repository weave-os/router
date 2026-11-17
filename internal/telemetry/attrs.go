package telemetry

type Attr struct {
	Key   string
	Value AttrValue
}

type AttrValue struct {
	kind      attrValueKind
	stringVal string
	intVal    int64
	floatVal  float64
	boolVal   bool
	arrayVal  []AttrValue
}

type attrValueKind uint8

const (
	attrValueString attrValueKind = iota + 1
	attrValueInt
	attrValueFloat
	attrValueBool
	attrValueArray
)

func (v AttrValue) GetStringValue() string   { return v.stringVal }
func (v AttrValue) GetIntValue() int64       { return v.intVal }
func (v AttrValue) GetDoubleValue() float64  { return v.floatVal }
func (v AttrValue) GetBoolValue() bool       { return v.boolVal }
func (v AttrValue) ArrayValues() []AttrValue { return v.arrayVal }
func (v AttrValue) Kind() string {
	switch v.kind {
	case attrValueString:
		return "string"
	case attrValueInt:
		return "int"
	case attrValueFloat:
		return "float"
	case attrValueBool:
		return "bool"
	case attrValueArray:
		return "array"
	default:
		return ""
	}
}

type AttrBuilder struct {
	attrs []Attr
}

func NewAttrBuilder(cap int) *AttrBuilder {
	return &AttrBuilder{attrs: make([]Attr, 0, cap)}
}

func (b *AttrBuilder) String(key, val string) *AttrBuilder {
	b.attrs = append(b.attrs, Attr{Key: key, Value: AttrValue{kind: attrValueString, stringVal: val}})
	return b
}

func (b *AttrBuilder) Int64(key string, val int64) *AttrBuilder {
	b.attrs = append(b.attrs, Attr{Key: key, Value: AttrValue{kind: attrValueInt, intVal: val}})
	return b
}

func (b *AttrBuilder) Float64(key string, val float64) *AttrBuilder {
	b.attrs = append(b.attrs, Attr{Key: key, Value: AttrValue{kind: attrValueFloat, floatVal: val}})
	return b
}

func (b *AttrBuilder) Bool(key string, val bool) *AttrBuilder {
	b.attrs = append(b.attrs, Attr{Key: key, Value: AttrValue{kind: attrValueBool, boolVal: val}})
	return b
}

func (b *AttrBuilder) IntSlice(key string, vals []int) *AttrBuilder {
	values := make([]AttrValue, len(vals))
	for i, v := range vals {
		values[i] = AttrValue{kind: attrValueInt, intVal: int64(v)}
	}
	b.attrs = append(b.attrs, Attr{Key: key, Value: AttrValue{kind: attrValueArray, arrayVal: values}})
	return b
}

func (b *AttrBuilder) Build() []Attr {
	return b.attrs
}
