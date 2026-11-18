package protocol

import "strconv"

// IDValuePairs collects (id, value) pairs from a list of objects that each
// carry an id field and a value field -- a custom room's settings,
// {ID, Value} (custom_rooms.go). A value is paired with the last id before
// it; numeric values come back in decimal, strings as they are.
func IDValuePairs(payload []byte, idName, valueName string) map[int]string {
	out := map[int]string{}
	id, haveID := 0, false
	for _, s := range Scalars(payload) {
		switch s.Name {
		case idName:
			if s.IsStr {
				n, err := strconv.Atoi(s.Str)
				if err != nil {
					continue
				}
				id = n
			} else {
				id = int(s.Num)
			}
			haveID = true
		case valueName:
			if !haveID {
				continue
			}
			if s.IsStr {
				out[id] = s.Str
			} else {
				out[id] = strconv.FormatInt(s.Num, 10)
			}
			haveID = false
		}
	}
	return out
}
