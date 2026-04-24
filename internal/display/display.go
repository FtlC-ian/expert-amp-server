package display

const (
	Rows = 8
	Cols = 40
)

type State struct {
	Chars [Rows][Cols]byte `json:"chars"`
	Attrs [Rows][Cols]byte `json:"attrs"`
}

func NewState() State {
	state := State{}
	for row := 0; row < Rows; row++ {
		for col := 0; col < Cols; col++ {
			state.Chars[row][col] = 0x60
		}
	}
	return state
}

// SetRow encodes an ASCII string into the row as SPE protocol bytes (ROM indices).
// Printable ASCII characters 0x21–0x7E are stored as char-0x20 (the protocol
// encoding). Space (0x20) and NUL (0x00) map to the blank sentinel 0x60.
// This matches the encoding used by StateFromFrame so that DemoState and live
// frames use the same code space.
func (s *State) SetRow(row int, text string) {
	if row < 0 || row >= Rows {
		return
	}
	for col := 0; col < Cols; col++ {
		s.Chars[row][col] = 0x60
	}
	for col := 0; col < len(text) && col < Cols; col++ {
		c := text[col]
		if c >= 0x21 && c <= 0x7e {
			s.Chars[row][col] = c - 0x20
		} else {
			s.Chars[row][col] = 0x60 // space or non-printable -> blank
		}
	}
}

func (s *State) SetAttr(row int, col int, attr byte) {
	if row < 0 || row >= Rows || col < 0 || col >= Cols {
		return
	}
	s.Attrs[row][col] = attr
}

func DemoState() State {
	state := NewState()
	state.SetRow(0, "SOLID STATE")
	state.SetRow(1, "FULLY AUTOMATIC")
	state.SetRow(2, "SATURN SPE GO PREVIEW")
	state.SetRow(3, "FREQ 14.074.000")
	state.SetRow(4, "ANT 1   ATU READY")
	state.SetRow(5, "CLIENT REMOTE OVER IP")
	state.SetRow(6, "STANDBY")
	state.SetRow(7, "SETUP CAT FEATURES")

	for col := 0; col < 6; col++ {
		state.SetAttr(0, col, 0x01)
	}
	for col := 0; col < 6; col++ {
		state.SetAttr(7, 34+col, 0x80)
	}

	return state
}

func DemoStateAlt() State {
	state := DemoState()
	state.SetRow(3, "FREQ 14.075.500")
	state.SetRow(4, "ANT 2   ATU TUNING")
	state.SetRow(6, "OPERATE")
	state.SetRow(7, "SETUP CAT FEATURES>")

	for row := 0; row < Rows; row++ {
		for col := 0; col < Cols; col++ {
			state.SetAttr(row, col, 0x00)
		}
	}
	for col := 0; col < 7; col++ {
		state.SetAttr(6, col, 0x01)
	}
	for col := 0; col < 8; col++ {
		state.SetAttr(7, 32+col, 0x80)
	}

	return state
}
