package parse

import "fmt"

// errStreamUnsupported: ParseObject parses a single direct object and does not
// read stream data. Use ParseIndirectObjectAt for objects that may be streams.
var errStreamUnsupported = fmt.Errorf("%w: streams are not supported by ParseObject; use ParseIndirectObjectAt", errLex)

// ParseObject reads exactly ONE direct object from data: Integer, Real,
// Boolean, Null, String, Name, Array, Dict, or Reference (`n g R`). Trailing
// tokens after the object are ignored (lenient). It does not handle streams or
// indirect-object definitions (`n g obj … endobj`); those are resolved through
// the xref layer (see Resolver and ParseIndirectObjectAt).
//
// It returns the parsed Object, or errStreamUnsupported if the object turns out
// to be a stream, or a wrapped errLex on any other syntax error.
func ParseObject(data []byte) (Object, error) {
	p := &iparser{data: data, pos: 0}
	obj, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if st, ok := obj.(*Stream); ok {
		_ = st
		return nil, errStreamUnsupported
	}
	return obj, nil
}
