// Port of svgtypes 0.16.1 src/viewbox.rs

package usvg

// stViewBoxError is ViewBoxError.
type stViewBoxError uint8

const (
	stViewBoxInvalidNumber stViewBoxError = iota // one of the numbers is invalid
	stViewBoxInvalidSize                         // negative or zero size
)

func (e stViewBoxError) Error() string {
	if e == stViewBoxInvalidNumber {
		return "viewBox contains an invalid number"
	}
	return "viewBox has a negative or zero size"
}

// stViewBox is ViewBox, the <viewBox> type.
type stViewBox struct {
	X, Y, W, H float64
}

// stNewViewBox is ViewBox::new.
func stNewViewBox(x, y, w, h float64) stViewBox { return stViewBox{x, y, w, h} }

// stParseViewBox is ViewBox::from_str. Trailing data after four numbers is ignored, as in Rust.
func stParseViewBox(text string) (stViewBox, error) {
	s := stNewStream(text)
	var v [4]float64
	for i := range v {
		n, err := s.parseListNumber()
		if err != nil {
			return stViewBox{}, stViewBoxInvalidNumber
		}
		v[i] = n
	}
	if v[2] <= 0 || v[3] <= 0 {
		return stViewBox{}, stViewBoxInvalidSize
	}
	return stViewBox{v[0], v[1], v[2], v[3]}, nil
}
