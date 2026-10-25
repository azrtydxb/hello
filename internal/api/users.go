package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// Users and roles (spec S-23) and user accounts: an admin creates, edits,
// disables and deletes users, links them to extensions and sets or resets
// their local password. A password and its hash never appear in a response,
// a log line or an audit row.

// loginRe is what a login may be: a username or an email address.
var loginRe = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,254}$`)

var afterActions = map[string]bool{"none": true, "mark_read": true, "delete": true}

func (s *server) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.internal(w, "list users", err)
		return
	}
	writeJSON(w, http.StatusOK, list[store.User]{Items: us})
}

func (s *server) getUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	u, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.storeError(w, "user", err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// vmPrefsIn is the voicemail-email options as sent; every field is optional.
type vmPrefsIn struct {
	Notify *bool   `json:"notify"`
	Attach *bool   `json:"attach"`
	After  *string `json:"after"`
}

func (p *vmPrefsIn) validate(f *fieldErrs, path string) {
	if p != nil && p.After != nil && !afterActions[*p.After] {
		f.add(path+".after", "must be none, mark_read or delete")
	}
}

// merge applies p over base.
func (p *vmPrefsIn) merge(base store.VoicemailEmailPrefs) store.VoicemailEmailPrefs {
	if p == nil {
		return base
	}
	if p.Notify != nil {
		base.Notify = *p.Notify
	}
	if p.Attach != nil {
		base.Attach = *p.Attach
	}
	if p.After != nil {
		base.After = *p.After
	}
	return base
}

// validEmail checks an optional email address: empty or well-formed.
func validEmail(f *fieldErrs, path, v string) {
	if v != "" && (len(v) > 254 || !emailRe.MatchString(v)) {
		f.add(path, "must be an email address or empty")
	}
}

func (s *server) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username       string     `json:"username"`
		DisplayName    string     `json:"displayName"`
		Email          string     `json:"email"`
		AuthMethod     string     `json:"authMethod"`
		Role           string     `json:"role"`
		Password       string     `json:"password"`
		MustChange     *bool      `json:"mustChangePassword"`
		ExtensionIDs   []int64    `json:"extensionIds"`
		VoicemailEmail *vmPrefsIn `json:"voicemailEmail"`
	}
	if !decode(w, r, &in) {
		return
	}
	var f fieldErrs
	if !loginRe.MatchString(in.Username) {
		f.add("username", "must be 1-254 of letters, digits and . _ @ + -")
	}
	role, err := auth.ParseRole(in.Role)
	if err != nil {
		f.add("role", "must be user, viewer, operator or admin")
	}
	if in.AuthMethod != "" && in.AuthMethod != "local" {
		f.add("authMethod", "must be local")
	}
	if err := auth.ValidatePassword(in.Password); err != nil {
		f.add("password", "%s", strings.TrimPrefix(err.Error(), auth.ErrPasswordPolicy.Error()+": "))
	}
	name := strings.TrimSpace(in.DisplayName)
	if name != "" {
		var ok bool
		if name, ok = validName(name); !ok {
			f.add("displayName", "must be 1-100 printable characters")
		}
	}
	validEmail(&f, "email", in.Email)
	in.VoicemailEmail.validate(&f, "voicemailEmail")
	if len(in.ExtensionIDs) > 32 {
		f.add("extensionIds", "at most 32 extensions")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	hash, err := auth.HashUserPassword(in.Password)
	if err != nil {
		s.internal(w, "create user: hash password", err)
		return
	}
	// The admin chose the password, so the user must replace it, unless the
	// admin says otherwise.
	must := in.MustChange == nil || *in.MustChange
	u, err := s.Store.CreateAccount(r.Context(), actor(r).String(), store.NewAccount{
		Username: in.Username, DisplayName: name, Email: in.Email, Role: role, PasswordHash: hash, MustChange: must,
		ExtensionIDs: in.ExtensionIDs, Voicemail: in.VoicemailEmail.merge(store.VoicemailEmailPrefs{Notify: true, Attach: true, After: "none"}),
	})
	if err != nil {
		s.configError(w, "user", err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Role           *string         `json:"role"`
		DisplayName    *string         `json:"displayName"`
		Email          *string         `json:"email"`
		Enabled        *bool           `json:"enabled"`
		Password       *string         `json:"password"`
		MustChange     *bool           `json:"mustChangePassword"`
		ExtensionID    json.RawMessage `json:"extensionId"`
		ExtensionIDs   *[]int64        `json:"extensionIds"`
		VoicemailEmail *vmPrefsIn      `json:"voicemailEmail"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Role == nil && in.DisplayName == nil && in.Email == nil && in.Enabled == nil && in.Password == nil &&
		in.ExtensionID == nil && in.ExtensionIDs == nil && in.VoicemailEmail == nil {
		badRequest(w, "a field to change is required")
		return
	}
	if in.ExtensionID != nil && in.ExtensionIDs != nil {
		badRequest(w, "give extensionId or extensionIds, not both")
		return
	}
	var (
		c   store.AccountChange
		f   fieldErrs
		cur store.User
	)
	if in.Role != nil {
		role, err := auth.ParseRole(*in.Role)
		if err != nil {
			badRequest(w, "role must be user, viewer, operator or admin")
			return
		}
		c.Role = &role
	}
	if in.DisplayName != nil {
		name := strings.TrimSpace(*in.DisplayName)
		if name != "" {
			var ok bool
			if name, ok = validName(name); !ok {
				f.add("displayName", "must be 1-100 printable characters")
			}
		}
		c.DisplayName = &name
	}
	if in.Email != nil {
		validEmail(&f, "email", *in.Email)
		c.Email = in.Email
	}
	c.Enabled = in.Enabled
	if in.Password != nil {
		if err := auth.ValidatePassword(*in.Password); err != nil {
			f.add("password", "%s", strings.TrimPrefix(err.Error(), auth.ErrPasswordPolicy.Error()+": "))
		} else if h, err := auth.HashUserPassword(*in.Password); err != nil {
			s.internal(w, "update user: hash password", err)
			return
		} else {
			c.PasswordHash, c.MustChange = &h, in.MustChange == nil || *in.MustChange
		}
	} else if in.MustChange != nil {
		f.add("mustChangePassword", "only applies together with password")
	}
	in.VoicemailEmail.validate(&f, "voicemailEmail")
	if in.VoicemailEmail != nil {
		u, err := s.Store.GetUser(r.Context(), id)
		if err != nil {
			s.storeError(w, "user", err)
			return
		}
		cur = u
		p := in.VoicemailEmail.merge(cur.VoicemailEmail)
		c.Voicemail = &p
	}
	switch {
	case in.ExtensionIDs != nil:
		if len(*in.ExtensionIDs) > 32 {
			f.add("extensionIds", "at most 32 extensions")
		}
		c.ExtensionIDs = in.ExtensionIDs
	case in.ExtensionID != nil:
		// extensionId links the user to one extension (spec P-3), replacing
		// any others; null unlinks them all.
		ids := []int64{}
		if string(in.ExtensionID) != "null" {
			var e int64
			if err := json.Unmarshal(in.ExtensionID, &e); err != nil || e <= 0 {
				badRequest(w, "extensionId must be an extension id or null")
				return
			}
			ids = append(ids, e)
		}
		c.ExtensionIDs = &ids
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	// An admin cannot lock themselves out by disabling their own account.
	if a := actor(r); a.UserID == id && c.Enabled != nil && !*c.Enabled {
		writeError(w, http.StatusConflict, "self_disable", "you cannot disable your own account")
		return
	}
	u, err := s.Store.UpdateAccount(r.Context(), actor(r).String(), id, c)
	if errors.Is(err, store.ErrLastAdmin) {
		writeError(w, http.StatusConflict, "last_admin", "at least one enabled user must remain admin")
		return
	}
	if err != nil {
		s.configError(w, "user", err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if actor(r).UserID == id {
		writeError(w, http.StatusConflict, "self_delete", "you cannot delete your own account")
		return
	}
	err := s.Store.DeleteUser(r.Context(), actor(r).String(), id)
	if errors.Is(err, store.ErrLastAdmin) {
		writeError(w, http.StatusConflict, "last_admin", "at least one enabled user must remain admin")
		return
	}
	if err != nil {
		s.storeError(w, "user", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
