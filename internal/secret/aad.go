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

// DoorCameraAAD is the additional data a door station camera's password is
// sealed under, so a value copied to another extension's row does not open.
func DoorCameraAAD(extensionID int64) string { return fmt.Sprintf("door-camera:%d", extensionID) }

// DoorWebhookAAD is the additional data a doorbell webhook's secret is sealed
// under, so a value copied to another webhook's row does not open.
func DoorWebhookAAD(id int64) string { return fmt.Sprintf("door-webhook:%d", id) }

// CRMSecretAAD is the additional data a CRM connector's OAuth client secret
// is sealed under.
func CRMSecretAAD(connectorID int64) string { return fmt.Sprintf("crm:%d:client_secret", connectorID) }

// CRMTokenAAD is the additional data a CRM connection's access or refresh
// token is sealed under (which is "access" or "refresh").
func CRMTokenAAD(connectorID int64, which string) string {
	return fmt.Sprintf("crm:%d:%s", connectorID, which)
}
