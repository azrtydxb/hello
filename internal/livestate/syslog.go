// Phone syslog lines (phone-feature-parity S-27): the last lines per phone,
// and per unknown source address, that hello-control's syslog sink received.
// The sink writes them; the API reads them.
package livestate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// SyslogLine is one stored log line.
type SyslogLine struct {
	// At is when Hello received the line.
	At time.Time `json:"at"`
	// Source is the sender's IP address.
	Source string `json:"source"`
	// Severity is the syslog severity 0 (emergency) to 7 (debug), -1 when the
	// line carried no priority.
	Severity int `json:"severity"`
	// Message is the line after redaction, without its priority.
	Message string `json:"message"`
}

// Retention: the last SyslogPhoneCap lines per phone and SyslogUnknownCap per unknown
// source address, each list expiring SyslogTTL after its newest line.
const (
	SyslogPhoneCap   = 10000
	SyslogUnknownCap = 1000
	SyslogTTL        = 24 * time.Hour
	// SyslogMaxUnknown bounds how many unknown source addresses are indexed.
	SyslogMaxUnknown = 200
)

const (
	syslogPhonePrefix   = "hello:syslog:phone:"
	syslogUnknownPrefix = "hello:syslog:ip:"
	syslogUnknownIndex  = "hello:syslog:unknown"
)

// SyslogPhoneKey is the list key of a phone's lines.
func SyslogPhoneKey(phoneID int64) string { return syslogPhonePrefix + strconv.FormatInt(phoneID, 10) }

// SyslogUnknownKey is the list key of the lines from an address no phone owns.
func SyslogUnknownKey(ip string) string { return syslogUnknownPrefix + ip }

// syslogPushScript pushes ARGV[1] to the head of KEYS[1], trims it to ARGV[2]
// lines and sets its expiry to ARGV[3] ms, so a list is never unbounded or
// without a TTL.
var syslogPushScript = valkey.NewLuaScript(`
redis.call('LPUSH', KEYS[1], ARGV[1])
redis.call('LTRIM', KEYS[1], 0, tonumber(ARGV[2]) - 1)
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 1`)

// syslogUnknownScript also records the source in the unknown-sources index (a
// sorted set by last line, bounded and expired like the lists).
var syslogUnknownScript = valkey.NewLuaScript(`
redis.call('LPUSH', KEYS[1], ARGV[1])
redis.call('LTRIM', KEYS[1], 0, tonumber(ARGV[2]) - 1)
redis.call('PEXPIRE', KEYS[1], ARGV[3])
redis.call('ZADD', KEYS[2], ARGV[4], ARGV[5])
local gone = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', tonumber(ARGV[4]) - tonumber(ARGV[3]))
for _, ip in ipairs(gone) do redis.call('DEL', ARGV[7] .. ip) end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', tonumber(ARGV[4]) - tonumber(ARGV[3]))
local over = redis.call('ZRANGE', KEYS[2], 0, -tonumber(ARGV[6]) - 1)
for _, ip in ipairs(over) do redis.call('DEL', ARGV[7] .. ip) end
redis.call('ZREMRANGEBYRANK', KEYS[2], 0, -tonumber(ARGV[6]) - 1)
redis.call('PEXPIRE', KEYS[2], ARGV[3])
return 1`)

// AppendSyslogPhone stores l among a phone's lines.
func (s *Store) AppendSyslogPhone(ctx context.Context, phoneID int64, l SyslogLine) error {
	v, err := json.Marshal(l)
	if err != nil {
		return err
	}
	err = syslogPushScript.Exec(ctx, s.c, []string{SyslogPhoneKey(phoneID)}, []string{
		string(v), strconv.Itoa(SyslogPhoneCap), strconv.FormatInt(SyslogTTL.Milliseconds(), 10),
	}).Error()
	if err != nil {
		return fmt.Errorf("livestate: store phone line: %w", err)
	}
	return nil
}

// AppendSyslogUnknown stores l among the lines of its source address, which no
// phone owns.
func (s *Store) AppendSyslogUnknown(ctx context.Context, l SyslogLine) error {
	v, err := json.Marshal(l)
	if err != nil {
		return err
	}
	err = syslogUnknownScript.Exec(ctx, s.c, []string{SyslogUnknownKey(l.Source), syslogUnknownIndex}, []string{
		string(v), strconv.Itoa(SyslogUnknownCap), strconv.FormatInt(SyslogTTL.Milliseconds(), 10),
		strconv.FormatInt(l.At.UnixMilli(), 10), l.Source, strconv.Itoa(SyslogMaxUnknown), syslogUnknownPrefix,
	}).Error()
	if err != nil {
		return fmt.Errorf("livestate: store unknown line: %w", err)
	}
	return nil
}

// SyslogPhoneLines returns up to limit of a phone's lines, newest first.
func (s *Store) SyslogPhoneLines(ctx context.Context, phoneID int64, limit int) ([]SyslogLine, error) {
	return s.syslogLines(ctx, SyslogPhoneKey(phoneID), limit)
}

// SyslogUnknownLines returns up to limit lines of an unknown source, newest first.
func (s *Store) SyslogUnknownLines(ctx context.Context, ip string, limit int) ([]SyslogLine, error) {
	return s.syslogLines(ctx, SyslogUnknownKey(ip), limit)
}

// SyslogUnknownSources lists the addresses that sent lines no phone owns, newest
// first.
func (s *Store) SyslogUnknownSources(ctx context.Context) ([]string, error) {
	out, err := s.c.Do(ctx, s.c.B().Zrevrange().Key(syslogUnknownIndex).Start(0).Stop(SyslogMaxUnknown-1).Build()).AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("livestate: unknown sources: %w", err)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

func (s *Store) syslogLines(ctx context.Context, key string, limit int) ([]SyslogLine, error) {
	if limit < 1 || limit > SyslogPhoneCap {
		limit = SyslogPhoneCap
	}
	vs, err := s.c.Do(ctx, s.c.B().Lrange().Key(key).Start(0).Stop(int64(limit-1)).Build()).AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("livestate: read lines: %w", err)
	}
	out := make([]SyslogLine, 0, len(vs))
	for _, v := range vs {
		var l SyslogLine
		if err := json.Unmarshal([]byte(v), &l); err != nil {
			return nil, fmt.Errorf("livestate: decode line: %w", err)
		}
		out = append(out, l)
	}
	return out, nil
}
