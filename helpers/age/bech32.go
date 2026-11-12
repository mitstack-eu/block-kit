package age

import (
	"errors"
	"fmt"
	"strings"
)

const bech32Alphabet = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var bech32Generator = [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}

func bech32Polymod(values []byte) uint32 {
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i, g := range bech32Generator {
			if top>>uint(i)&1 == 1 {
				chk ^= g
			}
		}
	}
	return chk
}

func bech32Expand(hrp string) []byte {
	out := make([]byte, 0, 2*len(hrp)+1)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]>>5)
	}
	out = append(out, 0)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]&31)
	}
	return out
}

func convertBits(data []byte, from, to uint, pad bool) ([]byte, error) {
	var acc, bitCount uint
	var out []byte
	max := uint(1)<<to - 1
	for _, v := range data {
		if uint(v)>>from != 0 {
			return nil, errors.New("a value is outside its bit width")
		}
		acc = acc<<from | uint(v)
		bitCount += from
		for bitCount >= to {
			bitCount -= to
			out = append(out, byte(acc>>bitCount&max))
		}
	}
	switch {
	case pad && bitCount > 0:
		out = append(out, byte(acc<<(to-bitCount)&max))
	case !pad && (bitCount >= from || acc<<(to-bitCount)&max != 0):
		return nil, errors.New("the padding is not zero")
	}
	return out, nil
}

func bech32Encode(hrp string, data []byte) (string, error) {
	five, err := convertBits(data, 8, 5, true)
	if err != nil {
		return "", err
	}
	values := append(bech32Expand(hrp), five...)
	mod := bech32Polymod(append(values, 0, 0, 0, 0, 0, 0)) ^ 1
	var b strings.Builder
	b.WriteString(hrp)
	b.WriteByte('1')
	for _, v := range five {
		b.WriteByte(bech32Alphabet[v])
	}
	for i := 0; i < 6; i++ {
		b.WriteByte(bech32Alphabet[mod>>(5*uint(5-i))&31])
	}
	return b.String(), nil
}

// bech32Decode reads a string of one case, returning its lowercase human-readable part and its data.
func bech32Decode(s string) (hrp string, data []byte, err error) {
	if s != strings.ToLower(s) && s != strings.ToUpper(s) {
		return "", nil, errors.New("the string mixes upper and lower case")
	}
	s = strings.ToLower(s)
	sep := strings.LastIndexByte(s, '1')
	if sep < 1 || len(s)-sep-1 < 6 {
		return "", nil, errors.New("the string has no separator, human-readable part or checksum")
	}
	hrp = s[:sep]
	values := make([]byte, 0, len(s)-sep-1)
	for _, c := range s[sep+1:] {
		i := strings.IndexRune(bech32Alphabet, c)
		if i < 0 {
			return "", nil, fmt.Errorf("the character %q is not in the bech32 alphabet", c)
		}
		values = append(values, byte(i))
	}
	if bech32Polymod(append(bech32Expand(hrp), values...)) != 1 {
		return "", nil, errors.New("the checksum does not match")
	}
	data, err = convertBits(values[:len(values)-6], 5, 8, false)
	if err != nil {
		return "", nil, err
	}
	return hrp, data, nil
}
