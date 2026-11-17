// Port of svgtypes 0.16.1 src/angle.rs

package usvg

// stAngleUnit is AngleUnit.
type stAngleUnit uint8

const (
	stAngleUnitDegrees stAngleUnit = iota
	stAngleUnitGradians
	stAngleUnitRadians
	stAngleUnitTurns
)

// stAngle is Angle, the <angle> type.
type stAngle struct {
	Number float64
	Unit   stAngleUnit
}

// stNewAngle is Angle::new.
func stNewAngle(number float64, unit stAngleUnit) stAngle { return stAngle{number, unit} }

// ToDegrees is Angle::to_degrees.
func (a stAngle) ToDegrees() float64 {
	switch a.Unit {
	case stAngleUnitGradians:
		return a.Number * 180.0 / 200.0
	case stAngleUnitRadians:
		return stToDegrees(a.Number)
	case stAngleUnitTurns:
		return a.Number * 360.0
	}
	return a.Number
}

// stParseAngle is Angle::from_str. Trailing spaces are an error.
func stParseAngle(text string) (stAngle, error) {
	s := stNewStream(text)
	a, err := s.parseAngle()
	if err != nil {
		return stAngle{}, err
	}
	if !s.atEnd() {
		return stAngle{}, stErrData(s.calcCharPos())
	}
	return a, nil
}

// parseAngle parses an angle; the unit is optional (degrees) and must be lowercase.
func (s *stStream) parseAngle() (stAngle, error) {
	s.skipSpaces()
	n, err := s.parseNumber()
	if err != nil {
		return stAngle{}, err
	}
	if s.atEnd() {
		return stAngle{n, stAngleUnitDegrees}, nil
	}
	u := stAngleUnitDegrees
	switch {
	case s.startsWith("deg"):
		s.advance(3)
	case s.startsWith("grad"):
		s.advance(4)
		u = stAngleUnitGradians
	case s.startsWith("rad"):
		s.advance(3)
		u = stAngleUnitRadians
	case s.startsWith("turn"):
		s.advance(4)
		u = stAngleUnitTurns
	}
	return stAngle{n, u}, nil
}
