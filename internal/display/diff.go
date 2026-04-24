package display

type CellDiff struct {
	Row  int  `json:"row"`
	Col  int  `json:"col"`
	Char byte `json:"char"`
	Attr byte `json:"attr"`
}

type Diff struct {
	Changes []CellDiff `json:"changes"`
}

func Compare(a State, b State) Diff {
	changes := make([]CellDiff, 0)
	for row := 0; row < Rows; row++ {
		for col := 0; col < Cols; col++ {
			if a.Chars[row][col] != b.Chars[row][col] || a.Attrs[row][col] != b.Attrs[row][col] {
				changes = append(changes, CellDiff{
					Row:  row,
					Col:  col,
					Char: b.Chars[row][col],
					Attr: b.Attrs[row][col],
				})
			}
		}
	}
	return Diff{Changes: changes}
}
