package translate

import (
	"unsafe"

	"github.com/tidwall/gjson"
)

func viewJSONPath(body []byte, path string) gjson.Result {
	if len(body) == 0 {
		return gjson.Result{}
	}
	return gjson.Get(unsafe.String(unsafe.SliceData(body), len(body)), path)
}

func viewJSON(body []byte) gjson.Result {
	if len(body) == 0 {
		return gjson.Result{}
	}
	return gjson.Parse(unsafe.String(unsafe.SliceData(body), len(body)))
}
