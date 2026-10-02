package routing

// Schedules name IANA time zones, and the runtime images ship no zoneinfo
// files, so embed the database (~450 KB) in every binary that routes.
import _ "time/tzdata"
