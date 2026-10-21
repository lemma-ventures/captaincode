package presence

import (
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf8"
)

type cborMap map[any]any

type cborReader struct {
	data       []byte
	pos, nodes int
}

func decodeCBOR(data []byte) (any, int, error) {
	if len(data) > 65536 {
		return nil, 0, fmt.Errorf("CBOR size")
	}
	r := cborReader{data: data}
	v, e := r.read(0)
	return v, r.pos, e
}

func (r *cborReader) take(n int) ([]byte, error) {
	if n < 0 || n > len(r.data)-r.pos {
		return nil, fmt.Errorf("CBOR truncated")
	}
	b := r.data[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}

func (r *cborReader) read(depth int) (any, error) {
	r.nodes++
	if depth > 8 || r.nodes > 256 {
		return nil, fmt.Errorf("CBOR complexity")
	}
	b, e := r.take(1)
	if e != nil {
		return nil, e
	}
	major, ai := b[0]>>5, b[0]&31
	var n uint64
	switch {
	case ai < 24:
		n = uint64(ai)
	case ai <= 27:
		size := 1 << int(ai-24)
		v, e := r.take(size)
		if e != nil {
			return nil, e
		}
		switch size {
		case 1:
			n = uint64(v[0])
		case 2:
			n = uint64(binary.BigEndian.Uint16(v))
		case 4:
			n = uint64(binary.BigEndian.Uint32(v))
		case 8:
			n = binary.BigEndian.Uint64(v)
		}
	default:
		return nil, fmt.Errorf("CBOR indefinite or reserved")
	}
	if n > math.MaxInt64 {
		return nil, fmt.Errorf("CBOR integer overflow")
	}
	switch major {
	case 0:
		return int64(n), nil
	case 1:
		return -1 - int64(n), nil
	case 2, 3:
		if n > 65536 {
			return nil, fmt.Errorf("CBOR string size")
		}
		v, e := r.take(int(n))
		if e != nil {
			return nil, e
		}
		if major == 2 {
			return v, nil
		}
		if !utf8.Valid(v) {
			return nil, fmt.Errorf("CBOR UTF-8")
		}
		return string(v), nil
	case 4:
		if n > 64 {
			return nil, fmt.Errorf("CBOR array size")
		}
		a := make([]any, 0, int(n))
		for i := uint64(0); i < n; i++ {
			v, e := r.read(depth + 1)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		return a, nil
	case 5:
		if n > 64 {
			return nil, fmt.Errorf("CBOR map size")
		}
		m := cborMap{}
		for i := uint64(0); i < n; i++ {
			k, e := r.read(depth + 1)
			if e != nil {
				return nil, e
			}
			switch k.(type) {
			case int64, string:
			default:
				return nil, fmt.Errorf("CBOR map key")
			}
			if _, ok := m[k]; ok {
				return nil, fmt.Errorf("CBOR duplicate key")
			}
			v, e := r.read(depth + 1)
			if e != nil {
				return nil, e
			}
			m[k] = v
		}
		return m, nil
	case 7:
		switch ai {
		case 20:
			return false, nil
		case 21:
			return true, nil
		case 22:
			return nil, nil
		}
	}
	return nil, fmt.Errorf("CBOR unsupported type")
}
