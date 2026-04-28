// Package embed handles the BLOB encoding for chunk embeddings and
// in-memory cosine similarity. We deliberately store float32 little-endian
// blobs and compute cosine in Go (same approach as graphrag) — fine up to
// ~50k chunks; if and when we outgrow it we plug in sqlite-vec.
package embed

import (
	"encoding/binary"
	"errors"
	"math"
)

// ErrLengthMismatch is returned when two vectors of different sizes are compared.
var ErrLengthMismatch = errors.New("embed: vector length mismatch")

// Encode serialises a float32 vector as little-endian bytes (4 per element).
// The result is what gets stored in chunks.embedding.
func Encode(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	out := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(f))
	}
	return out
}

// Decode is the inverse of Encode. Returns an error on malformed input
// (length not divisible by 4) so the caller can flag the row for reindex.
func Decode(b []byte) ([]float32, error) {
	if len(b) == 0 {
		return nil, nil
	}
	if len(b)%4 != 0 {
		return nil, errors.New("embed: blob length not multiple of 4")
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, nil
}

// Cosine returns the cosine similarity in [-1, 1]. Returns 0 (not an error)
// when either vector is the zero vector — that is the natural similarity
// for a vector with no information, and lets callers avoid divide-by-zero
// branching in hot loops.
func Cosine(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, ErrLengthMismatch
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0, nil
	}
	return dot / float32(math.Sqrt(float64(na))*math.Sqrt(float64(nb))), nil
}

// Normalize returns a unit-length copy of v. Useful when the caller is going
// to compute many dot products against v — pre-normalising both sides turns
// cosine into a single dot product.
func Normalize(v []float32) []float32 {
	if len(v) == 0 {
		return nil
	}
	var sumsq float64
	for _, x := range v {
		sumsq += float64(x) * float64(x)
	}
	if sumsq == 0 {
		return append([]float32(nil), v...)
	}
	inv := float32(1.0 / math.Sqrt(sumsq))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}
