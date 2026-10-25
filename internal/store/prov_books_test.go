package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
)

// TestDeliveredBooks (spec P-2) fails if a phone's delivered set is not the
// extensions book, the company book and its group's named books in the
// group's order; if a group's switches do not drop the first two; if a
// hidden extension is delivered; if RenderInputs does not point the phone at
// its books; or if an address-book fetch cannot be audited.
func TestDeliveredBooks(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	if _, err := st.CreateExtension(ctx, "test", "102", "Hidden", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE extensions SET directory_hidden = TRUE WHERE number = '102'`); err != nil {
		t.Fatal(err)
	}
	c, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000009", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: true, Realm: "hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	company := bookByKind(t, st, phonebook.KindCompany)
	if _, err := st.CreatePhonebookEntry(ctx, "test", company.ID, entry("Corp", "+3225550100")); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		bs, err := st.DeliveredBooks(ctx, c.Phone.ID)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, b := range bs {
			out = append(out, b.Name)
		}
		return out
	}
	if got := fmt.Sprint(names()); got != "[Extensions Company]" {
		t.Fatalf("ungrouped phone: %s", got)
	}
	bs, _ := st.DeliveredBooks(ctx, c.Phone.ID)
	if len(bs[0].Entries) != 1 || bs[0].Entries[0].LastName != "Sales" || len(bs[1].Entries) != 1 {
		t.Fatalf("entries (a hidden extension must not be delivered): %+v", bs)
	}

	wh, err := st.CreatePhonebook(ctx, "test", "Warehouse")
	if err != nil {
		t.Fatal(err)
	}
	ops, err := st.CreatePhonebook(ctx, "test", "Ops")
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.CreatePhoneGroup(ctx, "test", GroupInput{Name: "Site", IncludeExtensions: true, IncludeCompany: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetPhoneGroupBooks(ctx, "test", g.ID, []int64{ops.ID, wh.ID}); err != nil {
		t.Fatal(err)
	}
	grp := &g.ID
	if _, _, err := st.UpdatePhone(ctx, "test", c.Phone.ID, PhoneChange{GroupID: &grp}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(names()); got != "[Extensions Company Ops Warehouse]" {
		t.Fatalf("grouped phone: %s", got)
	}
	d, _, err := st.RenderInputs(ctx, c.Phone.ID)
	if err != nil && !errors.Is(err, prov.ErrNoTemplate) {
		t.Fatal(err)
	}
	if d.Book == nil || d.Book.URL != "https://prov.hello.test/p/"+c.Token+"/pb/0.xml" || d.Book.Sources[3].Name != "Warehouse" ||
		d.Book.Sources[4].URL != "" || d.Book.RefreshMinutes != 15 {
		t.Fatalf("book info %+v", d.Book)
	}

	if _, err := st.UpdatePhoneGroup(ctx, "test", g.ID, GroupInput{Name: "Site"}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(names()); got != "[Ops Warehouse]" {
		t.Fatalf("switches off: %s", got)
	}
	if err := st.InsertFetches(ctx, []prov.FetchRecord{{At: time.Now(), PhoneID: c.Phone.ID, PathRedacted: "/p/****/pb/0.xml",
		Kind: prov.KindPhonebook, Result: prov.ResultServed, Status: 200}}); err != nil {
		t.Fatalf("auditing an address-book fetch: %v", err)
	}
}
