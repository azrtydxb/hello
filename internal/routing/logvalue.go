package routing

import "log/slog"

// LogValue keeps the trunk password out of logs: a Trunk logged with slog
// shows whether a password is set, never its value.
func (t Trunk) LogValue() slog.Value {
	pw := "none"
	if t.Password != "" {
		pw = "configured"
	}
	return slog.GroupValue(
		slog.Int64("id", t.ID), slog.String("name", t.Name), slog.String("mode", t.Mode),
		slog.String("username", t.Username), slog.String("password", pw), slog.Bool("enabled", t.Enabled),
	)
}
