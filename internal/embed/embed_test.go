package embed

import (
	"errors"
	"math"
	"testing"
)

func TestEncodeDecode_roundtrip(t *testing.T) {
	in := []float32{0, 1, -1, 0.5, -0.25, math.MaxFloat32, math.SmallestNonzeroFloat32}
	out, err := Decode(Encode(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("len = %d", len(out))
	}
	for i := range in {
		if in[i] != out[i] {
			t.Errorf("idx %d: %v != %v", i, in[i], out[i])
		}
	}
}

func TestEncode_emptyAndNilSafe(t *testing.T) {
	if got := Encode(nil); got != nil {
		t.Errorf("expected nil")
	}
	out, err := Decode(nil)
	if err != nil || out != nil {
		t.Errorf("decode(nil): %v %v", out, err)
	}
}

func TestDecode_malformedFails(t *testing.T) {
	if _, err := Decode([]byte{1, 2, 3}); err == nil {
		t.Error("expected error on non-mod-4 input")
	}
}

func TestCosine_basic(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	c := []float32{0, 1, 0}
	d := []float32{-1, 0, 0}

	if v, _ := Cosine(a, b); !approxEqual(v, 1) {
		t.Errorf("self = %v", v)
	}
	if v, _ := Cosine(a, c); !approxEqual(v, 0) {
		t.Errorf("orth = %v", v)
	}
	if v, _ := Cosine(a, d); !approxEqual(v, -1) {
		t.Errorf("anti = %v", v)
	}
}

func TestCosine_zeroVector(t *testing.T) {
	a := []float32{0, 0}
	b := []float32{1, 1}
	v, err := Cosine(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0 {
		t.Errorf("zero-vector cosine = %v", v)
	}
}

func TestCosine_lengthMismatch(t *testing.T) {
	if _, err := Cosine([]float32{1}, []float32{1, 2}); !errors.Is(err, ErrLengthMismatch) {
		t.Errorf("err = %v", err)
	}
}

func TestNormalize_unitLength(t *testing.T) {
	v := Normalize([]float32{3, 4})
	var sumsq float32
	for _, x := range v {
		sumsq += x * x
	}
	if !approxEqual(sumsq, 1) {
		t.Errorf("not unit: sumsq = %v", sumsq)
	}
}

func TestNormalize_zeroVectorPassthrough(t *testing.T) {
	v := Normalize([]float32{0, 0})
	if len(v) != 2 || v[0] != 0 || v[1] != 0 {
		t.Errorf("zero-vector mangled: %v", v)
	}
}

func approxEqual(a, b float32) bool {
	if math.Abs(float64(a-b)) < 1e-5 {
		return true
	}
	return false
}
