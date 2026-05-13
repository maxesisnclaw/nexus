// Package shared contains msgpack helpers shared by demo Go services.
package shared

import (
	"github.com/vmihailenco/msgpack/v5"
)

// Pack encodes v as msgpack bytes, mapping struct fields by their `msgpack` tag
// (which matches the dict keys used by the Python services).
func Pack(v any) []byte {
	b, err := msgpack.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// Unpack decodes msgpack bytes into v (must be pointer).
func Unpack(data []byte, v any) error {
	return msgpack.Unmarshal(data, v)
}
