package secret

import "fmt"

// ConferencePINAAD is the additional data a conference room's PIN is sealed
// under: it binds the ciphertext to the room and to which PIN it is ("user"
// or "leader"), so a value copied to another row or column does not open.
func ConferencePINAAD(roomID int64, which string) string {
	return fmt.Sprintf("conference:%d:%s", roomID, which)
}

// TOTPAAD is the additional data a user's TOTP secret is sealed under, so a
// secret copied to another user's row does not open.
func TOTPAAD(userID int64) string { return fmt.Sprintf("user:%d:totp", userID) }

// MsgChannelAAD is the additional data a messaging channel's carrier
// credentials are sealed under, so a value copied to another channel's row
// does not open.
func MsgChannelAAD(channelID int64) string {
	return fmt.Sprintf("msg_channel:%d:credentials", channelID)
}
