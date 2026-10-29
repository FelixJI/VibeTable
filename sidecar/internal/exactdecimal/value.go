// Package exactdecimal is the shared coefficient arithmetic for protected writes and datasets.
package exactdecimal

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"
)

const MaxScale = 18
const MaxDigits = 38

var ErrInvalid = errors.New("transaction.decimal_invalid")
var ErrOverflow = errors.New("transaction.decimal_overflow")

type Format struct {
	Storage string `json:"storage"`
	Scale   int    `json:"scale"`
	Digits  int    `json:"digits"`
}

func (f Format) Validate() error {
	if (f.Storage != "decimalText" && f.Storage != "integerUnits") || f.Scale < 0 || f.Scale > MaxScale || f.Digits < 1 || f.Digits > MaxDigits || f.Scale > f.Digits {
		return ErrInvalid
	}
	return nil
}

// Parse never rounds. Number storage contains integer minor units, not decimal major units.
func (f Format) Parse(value any) (*big.Int, error) {
	if f.Validate() != nil || value == nil {
		return nil, ErrInvalid
	}
	var text string
	if f.Storage == "integerUnits" {
		switch n := value.(type) {
		case json.Number:
			text = string(n)
		case float64:
			if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || math.Abs(n) > 1<<53-1 {
				return nil, ErrInvalid
			}
			text = strconv.FormatFloat(n, 'f', 0, 64)
		case int:
			text = strconv.Itoa(n)
		case int64:
			text = strconv.FormatInt(n, 10)
		default:
			return nil, ErrInvalid
		}
	} else {
		var ok bool
		text, ok = value.(string)
		if !ok {
			return nil, ErrInvalid
		}
	}
	if len(text) == 0 || len(text) > MaxDigits+MaxScale+3 {
		return nil, ErrInvalid
	}
	negative := strings.HasPrefix(text, "-")
	unsigned := strings.TrimPrefix(text, "-")
	parts := strings.Split(unsigned, ".")
	if len(parts) > 2 || parts[0] == "" || len(parts[0]) > 1 && parts[0][0] == '0' {
		return nil, ErrInvalid
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" {
			return nil, ErrInvalid
		}
	}
	if f.Storage == "integerUnits" && len(parts) != 1 {
		return nil, ErrInvalid
	}
	for _, digit := range parts[0] + fraction {
		if digit < '0' || digit > '9' {
			return nil, ErrInvalid
		}
	}
	if f.Storage == "decimalText" {
		if len(fraction) > f.Scale {
			return nil, ErrInvalid
		}
		fraction += strings.Repeat("0", f.Scale-len(fraction))
	}
	n, ok := new(big.Int).SetString(parts[0]+fraction, 10)
	if !ok {
		return nil, ErrInvalid
	}
	if negative {
		n.Neg(n)
	}
	if f.Storage == "integerUnits" && new(big.Int).Abs(new(big.Int).Set(n)).Cmp(big.NewInt(1<<53-1)) > 0 {
		return nil, ErrInvalid
	}
	if err := f.Check(n); err != nil {
		return nil, err
	}
	return n, nil
}

func (f Format) Check(n *big.Int) error {
	if f.Validate() != nil || n == nil {
		return ErrInvalid
	}
	if len(new(big.Int).Abs(new(big.Int).Set(n)).String()) > f.Digits {
		return ErrOverflow
	}
	return nil
}

// Add bounds every intermediate coefficient, including sums used by constraints.
func (f Format) Add(total, value *big.Int) error {
	if total == nil || value == nil {
		return ErrInvalid
	}
	next := new(big.Int).Add(total, value)
	if err := f.Check(next); err != nil {
		return err
	}
	total.Set(next)
	return nil
}

func (f Format) Text(n *big.Int) (string, error) {
	if err := f.Check(n); err != nil {
		return "", err
	}
	text := new(big.Int).Abs(new(big.Int).Set(n)).String()
	if f.Scale > 0 {
		if len(text) <= f.Scale {
			text = strings.Repeat("0", f.Scale-len(text)+1) + text
		}
		text = text[:len(text)-f.Scale] + "." + text[len(text)-f.Scale:]
	}
	if n.Sign() < 0 {
		text = "-" + text
	}
	return text, nil
}
