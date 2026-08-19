package system

import (
	"reflect"
	"unicode"

	"github.com/dop251/goja"
)

// tagMapper maps struct fields to JS names. It prefers an explicit `goja:"..."`
// tag and otherwise lower-cases leading acronyms (FS→fs, TCP→tcp) or the first
// letter of a camelCase name.
type tagMapper struct{}

func (tagMapper) FieldName(t reflect.Type, f reflect.StructField) string {
	if tag := f.Tag.Get("goja"); tag != "" {
		return tag
	}
	return lowerName(f.Name)
}

func (tagMapper) MethodName(_ reflect.Type, m reflect.Method) string {
	return lowerName(m.Name)
}

// lowerName lower-cases a leading acronym (all-caps run) or just the first
// letter. e.g. FS→fs, TCPHost→tcpHost, BinaryPath→binaryPath.
func lowerName(s string) string {
	if s == "" {
		return s
	}
	i := 0
	for i < len(s) && s[i] >= 'A' && s[i] <= 'Z' {
		i++
	}
	if i > 1 && i < len(s) && s[i] >= 'a' && s[i] <= 'z' {
		// All-caps run followed by a lowercase letter: keep the last upper as
		// the acronym boundary is not clean (e.g. HTTPResponse → httpResponse).
		i--
	}
	return string(unicode.ToLower(rune(s[0]))) + s[1:]
}

// newFieldNameMapper returns the field-name mapper used for host modules.
func newFieldNameMapper() goja.FieldNameMapper {
	return tagMapper{}
}
