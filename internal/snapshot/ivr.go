package snapshot

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// IVR destination kinds (migration 00033). The target's meaning depends on
// the kind: an extension number (extension, voicemail), a ring group name,
// an announcement name, a page code, an IVR menu name or an external number;
// hangup has none.
const (
	IVRHangup       = "hangup"
	IVRExtension    = "extension"
	IVRRingGroup    = "ring_group"
	IVRVoicemail    = "voicemail"
	IVRAnnouncement = "announcement"
	IVRPaging       = "paging"
	IVRMenuKind     = "ivr"
	IVRExternal     = "external"
	IVRQueue        = "queue"
	// IVRVoicemailGroup names a voicemail group by its number (migration
	// 00046): the message is left in every member's box.
	IVRVoicemailGroup = "voicemail_group"
	// IVRTimeCondition names a time condition (migration 00034).
	IVRTimeCondition = "time_condition"
)

// IVRDest is where a menu option, timeout or invalid choice sends the call.
type IVRDest struct {
	Kind, Target string
}

// IVRMenu is an IVR / auto-attendant menu (migration 00033). The prompts
// are announcement names (resolved with Snapshot.Announcement); an empty
// prompt name means none. Timeouts are seconds. Options maps a digit
// (0-9, * or #) to its destination.
type IVRMenu struct {
	ID                                       int64
	Name, Number                             string // Number dials the menu directly; empty means it is only a destination
	Enabled                                  bool
	Greeting, InvalidPrompt, TimeoutPrompt   string
	DigitTimeout, InterDigitTimeout, Retries int
	AllowDirectDial                          bool
	Timeout, Invalid                         IVRDest
	Options                                  map[string]IVRDest
}

// WithIVRMenus installs the IVR menus (tests and Load); it returns s.
func (s *Snapshot) WithIVRMenus(menus []IVRMenu) *Snapshot {
	if s.ivrByName == nil {
		s.ivrByName, s.ivrByNumber = map[string]IVRMenu{}, map[string]IVRMenu{}
	}
	for _, m := range menus {
		s.ivrByName[m.Name] = m
		if m.Number != "" {
			s.ivrByNumber[m.Number] = m
		}
	}
	return s
}

// IVRMenu returns the menu with that name.
func (s *Snapshot) IVRMenu(name string) (IVRMenu, bool) {
	m, ok := s.ivrByName[name]
	return m, ok
}

// IVRMenuByNumber returns the menu dialled as number.
func (s *Snapshot) IVRMenuByNumber(number string) (IVRMenu, bool) {
	if number == "" {
		return IVRMenu{}, false
	}
	m, ok := s.ivrByNumber[number]
	return m, ok
}

// ExtensionPrefix classifies digits a caller dialled from an IVR menu
// against the extension numbers: exact reports a number equal to digits,
// longer one that digits is a proper prefix of at least one number.
func (s *Snapshot) ExtensionPrefix(digits string) (exact, longer bool) {
	if digits == "" {
		return false, false
	}
	check := func(n string) {
		switch {
		case n == digits:
			exact = true
		case strings.HasPrefix(n, digits):
			longer = true
		}
	}
	for n := range s.known {
		check(n)
	}
	for n := range s.byExt {
		check(n)
	}
	for n := range s.exts {
		check(n)
	}
	return exact, longer
}

// loadIVRMenus reads the IVR menus with their options and the names of the
// announcements they play.
func loadIVRMenus(ctx context.Context, db Querier) ([]IVRMenu, error) {
	rows, err := db.Query(ctx, `SELECT m.id, m.name, m.number, m.enabled, g.name, COALESCE(i.name, ''), COALESCE(t.name, ''),
		m.digit_timeout, m.inter_digit_timeout, m.max_retries, m.allow_direct_dial,
		m.timeout_kind, m.timeout_target, m.invalid_kind, m.invalid_target
		FROM ivr_menus m
		JOIN announcements g ON g.id = m.greeting_id
		LEFT JOIN announcements i ON i.id = m.invalid_prompt_id
		LEFT JOIN announcements t ON t.id = m.timeout_prompt_id
		ORDER BY m.id`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: ivr menus: %w", err)
	}
	var menus []IVRMenu
	for rows.Next() {
		m := IVRMenu{Options: map[string]IVRDest{}}
		if err := rows.Scan(&m.ID, &m.Name, &m.Number, &m.Enabled, &m.Greeting, &m.InvalidPrompt, &m.TimeoutPrompt,
			&m.DigitTimeout, &m.InterDigitTimeout, &m.Retries, &m.AllowDirectDial,
			&m.Timeout.Kind, &m.Timeout.Target, &m.Invalid.Kind, &m.Invalid.Target); err != nil {
			rows.Close()
			return nil, fmt.Errorf("snapshot: scan ivr menu: %w", err)
		}
		menus = append(menus, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: ivr menus: %w", err)
	}
	if len(menus) == 0 {
		return nil, nil
	}
	byID := map[int64]int{}
	for i, m := range menus {
		byID[m.ID] = i
	}
	orows, err := db.Query(ctx, `SELECT menu_id, digit, dest_kind, dest_target FROM ivr_menu_options ORDER BY menu_id, digit`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: ivr options: %w", err)
	}
	defer orows.Close()
	for orows.Next() {
		var (
			id    int64
			digit string
			d     IVRDest
		)
		if err := orows.Scan(&id, &digit, &d.Kind, &d.Target); err != nil {
			return nil, fmt.Errorf("snapshot: scan ivr option: %w", err)
		}
		if i, ok := byID[id]; ok {
			menus[i].Options[digit] = d
		}
	}
	if err := orows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: ivr options: %w", err)
	}
	return menus, nil
}

// OptionDigits lists the menu's option digits in order.
func (m IVRMenu) OptionDigits() []string {
	out := make([]string, 0, len(m.Options))
	for d := range m.Options {
		out = append(out, d)
	}
	slices.Sort(out)
	return out
}
