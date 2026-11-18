// momo:fromdb sha256=6ef26bde97b619df3f29878f86e4b734f890a3f048ded154f486ca29f1c2a968
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// PhonebookEntry models the table "phonebook_entries".
type PhonebookEntry struct {
	momo.Model
	ID        int64 `momo:"pk,serial"`
	BookID    int64
	FirstName string `momo:"default:''::text,dbdefault"`
	LastName  string `momo:"default:''::text,dbdefault"`
	Company   string `momo:"default:''::text,dbdefault"`
	Numbers   types.JSON[json.RawMessage]
	Note      string    `momo:"default:''::text,dbdefault"`
	CreatedAt time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt time.Time `momo:"default:now(),dbdefault"`

	Book *Phonebook `momo:"belongs_to,fk:BookID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *PhonebookEntry) MomoConfig(c *momo.Config) {
	c.Check("phonebook_entries_company_check", "(char_length(company) <= 60)")
	c.Check("phonebook_entries_first_name_check", "(char_length(first_name) <= 60)")
	c.Check("phonebook_entries_last_name_check", "(char_length(last_name) <= 60)")
	c.Check("phonebook_entries_note_check", "(char_length(note) <= 255)")
	c.Check("phonebook_entries_numbers_check", "((jsonb_typeof(numbers) = 'array'::text) AND ((jsonb_array_length(numbers) >= 1) AND (jsonb_array_length(numbers) <= 3)))")
	c.IndexExpr("phonebook_entries_book_name", "book_id", "lower(last_name)", "lower(first_name)", "id")
}
