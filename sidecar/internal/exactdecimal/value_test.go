package exactdecimal

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"
)

func TestExactDecimal(t *testing.T) {
	f := Format{Storage: "decimalText", Scale: 2, Digits: 38}
	total := new(big.Int)
	for _, text := range []string{"9007199254740993.10", "0.20", "-9007199254740993.00"} {
		n, err := f.Parse(text)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Add(total, n); err != nil {
			t.Fatal(err)
		}
	}
	text, err := f.Text(total)
	if err != nil || text != "0.30" {
		t.Fatalf("%s %v", text, err)
	}
	for _, v := range []any{nil, 0.1, "1.001", "1e2", "01.00", "+1", "NaN", "", "1.", " 1"} {
		if _, err := f.Parse(v); err == nil {
			t.Fatalf("accepted %#v", v)
		}
	}
	for _, s := range []int{-1, 19} {
		bad := f
		bad.Scale = s
		if bad.Validate() == nil {
			t.Fatal("invalid scale")
		}
	}
	n, _ := f.Parse("-0.00")
	text, _ = f.Text(n)
	if text != "0.00" {
		t.Fatal(text)
	}
	n, _ = f.Parse("0")
	text, _ = f.Text(n)
	if text != "0.00" {
		t.Fatal(text)
	}
}

func TestExactDecimalBoundsAndIntegerUnits(t *testing.T) {
	f := Format{Storage: "decimalText", Scale: 2, Digits: 3}
	n, _ := f.Parse("9.99")
	if err := f.Add(n, big.NewInt(1)); !errors.Is(err, ErrOverflow) || n.Int64() != 999 {
		t.Fatalf("overflow mutated %s: %v", n, err)
	}
	f = Format{Storage: "integerUnits", Scale: 2, Digits: 38}
	for _, v := range []any{json.Number("9007199254740992"), float64(9007199254740992), json.Number("1.5"), "100"} {
		if _, err := f.Parse(v); err == nil {
			t.Fatalf("accepted %#v", v)
		}
	}
	n, err := f.Parse(json.Number("9007199254740991"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Add(n, big.NewInt(1)); err != nil {
		t.Fatal(err)
	}
	text, err := f.Text(n)
	if err != nil || text != "90071992547409.92" {
		t.Fatalf("%s %v", text, err)
	}
}
