package webserver

import (
	"github.com/fragshq/mneme/schema"
	"github.com/jinzhu/copier"
	"github.com/theirish81/edjson"
)

// The DTOs carry the domain types themselves for JSON documents (see the
// x-go-type extensions in the spec). edjson's deep copy would rebuild a JSON
// Schema field by field, breaking its invariants (Type and Types set
// together), so schemas are copied by reference: handlers never modify them.
func init() {
	edjson.Converters.Add(copier.TypeConverter{
		SrcType: (*schema.JSONSchema)(nil),
		DstType: (*schema.JSONSchema)(nil),
		Fn:      func(src any) (any, error) { return src, nil },
	})
}
