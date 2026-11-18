// momo:fromdb sha256=332e78ad2e0f687000e7bb96a207cdc840352f6d3c76ab1bbdebc8f6e700abf3
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Location models the table "locations".
type Location struct {
	momo.Model
	ID          int64 `momo:"pk,serial"`
	Kind        string
	Name        string
	ParentID    *int64 `momo:"index:locations_parent"`
	Country     string `momo:"default:''::text,dbdefault"`
	State       string `momo:"default:''::text,dbdefault"`
	City        string `momo:"default:''::text,dbdefault"`
	Street      string `momo:"default:''::text,dbdefault"`
	HouseNumber string `momo:"default:''::text,dbdefault"`
	PostalCode  string `momo:"default:''::text,dbdefault"`
	Latitude    *float64
	Longitude   *float64
	RadiusM     *float64
	CreatedAt   time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt   time.Time `momo:"default:now(),dbdefault"`

	Parent *Location `momo:"belongs_to,fk:ParentID,on_delete:restrict"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Location) MomoConfig(c *momo.Config) {
	c.Check("locations_check", "((kind = 'site'::text) = (parent_id IS NULL))")
	c.Check("locations_check1", "((latitude IS NULL) = (longitude IS NULL))")
	c.Check("locations_check2", "((radius_m IS NULL) OR (latitude IS NOT NULL))")
	c.Check("locations_city_check", "(char_length(city) <= 100)")
	c.Check("locations_country_check", "((country = ''::text) OR (country ~ '^[A-Z]{2}$'::text))")
	c.Check("locations_house_number_check", "(char_length(house_number) <= 100)")
	c.CheckIn(&m.Kind,
		"floor",
		"room",
		"site",
	)
	c.Check("locations_latitude_check", "((latitude >= ('-90'::integer)::double precision) AND (latitude <= (90)::double precision))")
	c.Check("locations_longitude_check", "((longitude >= ('-180'::integer)::double precision) AND (longitude <= (180)::double precision))")
	c.Check("locations_name_check", "((char_length(name) >= 1) AND (char_length(name) <= 100))")
	c.Check("locations_postal_code_check", "(char_length(postal_code) <= 100)")
	c.Check("locations_radius_m_check", "((radius_m > (0)::double precision) AND (radius_m <= (1000000)::double precision))")
	c.Check("locations_state_check", "(char_length(state) <= 100)")
	c.Check("locations_street_check", "(char_length(street) <= 100)")
	c.IndexExpr("locations_name", "coalesce(parent_id, 0::bigint)", "lower(name)").Unique()
}
