package secret

import "fmt"

// ConferencePINAAD is the additional data a conference room's PIN is sealed
// under: it binds the ciphertext to the room and to which PIN it is ("user"
// or "leader"), so a value copied to another row or column does not open.
func ConferencePINAAD(roomID int64, which string) string {
	return fmt.Sprintf("conference:%d:%s", roomID, which)
}
