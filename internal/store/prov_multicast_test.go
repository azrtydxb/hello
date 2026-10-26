package store

import (
	"context"
	"errors"
	"testing"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
)

// TestPhonePTT fails if a WP8x6 without a group does not get PTT on with
// channel 1 (available, transmit, subscribe) on Grandstream's default
// address, if the catalog's first PTT channel, the group's or the phone's
// override does not set the address and codec in that order, if a group
// with PTT off does not switch it off, or if a group without channels does
// not get its default channel defined.
func TestPhonePTT(t *testing.T) {
	p := phonePTT(nil, nil, nil)
	if !p.Enabled || p.Address != "224.0.1.116:5001" || p.CodecGS != 0 || p.DefaultChannel != 1 {
		t.Fatalf("ungrouped default PTT = %+v", p)
	}
	if c := p.Channels[0]; c.Available != 1 || c.Transmit != 1 || c.Subscribe != 1 || c.Label != "Channel 1" || c.Base != 82001 {
		t.Fatalf("channel 1 = %+v", c)
	}
	if c := p.Channels[1]; c.Available != 0 {
		t.Fatalf("channel 2 is available: %+v", c)
	}

	ptts := []phonebook.Channel{
		{ID: 7, Address: "239.1.1.1", Port: 5001, Codec: phonebook.CodecG722, Kind: phonebook.KindPTT, Enabled: true},
		{ID: 8, Address: "239.1.1.2", Port: 5002, Codec: phonebook.CodecPCMU, Kind: phonebook.KindPTT, Enabled: true},
	}
	if p := phonePTT(nil, ptts, nil); p.Address != "239.1.1.1:5001" || p.CodecGS != 9 {
		t.Errorf("catalog's first PTT channel not used: %+v", p)
	}
	eight := int64(8)
	if p := phonePTT(nil, ptts, &eight); p.Address != "239.1.1.2:5002" || p.CodecGS != 0 {
		t.Errorf("phone override not used: %+v", p)
	}
	seven := int64(7)
	g := &phonebook.Group{PTT: phonebook.PTT{Enabled: true, ChannelID: &eight, DefaultChannel: 24, AcceptBusy: true,
		Channels: []phonebook.PTTChannel{{Channel: 24, Label: "Priority", Transmit: true}}}}
	p = phonePTT(g, ptts, nil)
	if p.Address != "239.1.1.2:5002" || p.DefaultChannel != 24 || p.AcceptBusy != 1 || p.Channels[23].Available != 1 || p.Channels[0].Available != 0 {
		t.Errorf("group PTT = %+v", p)
	}
	if p := phonePTT(g, ptts, &seven); p.Address != "239.1.1.1:5001" {
		t.Errorf("phone override does not beat the group's channel: %+v", p)
	}
	missing := int64(99)
	if p := phonePTT(&phonebook.Group{PTT: phonebook.PTT{Enabled: true, ChannelID: &missing, DefaultChannel: 2}}, ptts, nil); p.Address != "239.1.1.1:5001" ||
		p.Channels[1].Available != 1 || p.Channels[1].Transmit != 1 {
		t.Errorf("group with a disabled channel and no channels = %+v", p)
	}
	if p := phonePTT(&phonebook.Group{PTT: phonebook.PTT{Enabled: false}}, ptts, nil); p.Enabled {
		t.Errorf("group with PTT off gives PTT on: %+v", p)
	}
}

// TestMulticastCatalog fails if the migrated catalog lacks Grandstream's
// PTT channel, if a WP8x6 without a group is not provisioned PTT on it and
// to listen on every enabled paging channel, if a phone's PTT override or a
// group's PTT off is not provisioned, if a WP822 gets PTT, if a group's
// channel list does not replace the catalog default, or if the catalog
// accepts a duplicate address, a kind change of a channel in use, a delete
// of a channel a paging group sends to, or a PTT channel for paging.
func TestMulticastCatalog(t *testing.T) {
	st, _, ext := provStore(t)
	ctx := context.Background()
	all, err := st.ListMulticastChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Kind != phonebook.KindPTT || all[0].HostPort() != "224.0.1.116:5001" || !all[0].Enabled {
		t.Fatalf("migrated catalog = %+v", all)
	}
	gsPTT := all[0]
	on := true
	dock, err := st.CreateMulticastChannel(ctx, "test", phonebook.ChannelInput{Label: "Dock", Address: "239.255.0.1", Port: 5004,
		Codec: phonebook.CodecPCMU, Priority: 1, Kind: phonebook.KindPaging, Enabled: &on})
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := st.CreateMulticastChannel(ctx, "test", phonebook.ChannelInput{Label: "Off", Address: "239.255.0.3", Port: 5004,
		Codec: phonebook.CodecPCMU, Priority: 2, Kind: phonebook.KindPaging, Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	var v *ValidationError
	if _, err := st.CreateMulticastChannel(ctx, "test", phonebook.ChannelInput{Label: "Again", Address: "239.255.0.1", Port: 5004,
		Codec: phonebook.CodecPCMU, Priority: 1, Kind: phonebook.KindPaging}); !errors.As(err, &v) {
		t.Errorf("duplicate address accepted: %v", err)
	}

	c, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "c074ad000836", Vendor: prov.Grandstream, Model: "WP836",
		ExtensionID: ext, Enabled: true, Realm: "hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	var paging *prov.PagingOptions
	info := func(id int64, model string) (*prov.MulticastInfo, *prov.PTTInfo) {
		t.Helper()
		mc, ptt, opts, err := st.multicastInfo(ctx, id, prov.Grandstream, model)
		if err != nil {
			t.Fatal(err)
		}
		if opts == nil {
			t.Fatal("no paging options")
		}
		paging = opts
		return mc, ptt
	}
	mc, ptt := info(c.Phone.ID, "WP836")
	if ptt == nil || !ptt.Enabled || ptt.Address != "224.0.1.116:5001" || ptt.DefaultChannel != 1 || ptt.Channels[0].Available != 1 {
		t.Fatalf("ungrouped WP836 PTT = %+v", ptt)
	}
	if mc == nil || len(mc.Listen) != 1 || mc.Listen[0].HostPort != "239.255.0.1:5004" || !mc.PagingKey {
		t.Fatalf("ungrouped WP836 multicast = %+v", mc)
	}
	if ptt.GroupPaging || ptt.PriorityChannel != 24 || ptt.EmergencyChannel != 25 || ptt.Groups[0].Available != 0 {
		t.Errorf("ungrouped WP836 group paging = %+v", ptt)
	}
	if *paging != (prov.PagingOptions{PriorityActive: 1}) {
		t.Errorf("ungrouped paging options = %+v", *paging)
	}
	if _, ptt := info(c.Phone.ID, "WP822"); ptt == nil || !ptt.Enabled {
		t.Errorf("a WP822 gets no PTT: %+v", ptt)
	}
	if _, ptt := info(c.Phone.ID, "GXP2170"); ptt != nil {
		t.Errorf("a GXP2170 gets PTT: %+v", ptt)
	}

	// The phone's PTT override.
	site, err := st.CreateMulticastChannel(ctx, "test", phonebook.ChannelInput{Label: "Site PTT", Address: "239.1.1.1", Port: 5001,
		Codec: phonebook.CodecG722, Priority: 5, Kind: phonebook.KindPTT})
	if err != nil {
		t.Fatal(err)
	}
	pid := &site.ID
	if _, _, err := st.UpdatePhone(ctx, "test", c.Phone.ID, PhoneChange{PTTChannelID: &pid}); err != nil {
		t.Fatal(err)
	}
	if _, ptt := info(c.Phone.ID, "WP836"); ptt.Address != "239.1.1.1:5001" || ptt.CodecGS != 9 {
		t.Errorf("phone override not provisioned: %+v", ptt)
	}
	bad := &dock.ID
	if _, _, err := st.UpdatePhone(ctx, "test", c.Phone.ID, PhoneChange{PTTChannelID: &bad}); !errors.As(err, &v) {
		t.Errorf("a paging channel accepted as the PTT override: %v", err)
	}

	// A new group has PTT on with channel 1; its channel list replaces the
	// catalog default; PTT off is provisioned off.
	g, err := st.CreatePhoneGroup(ctx, "test", GroupInput{Name: "Warehouse"})
	if err != nil {
		t.Fatal(err)
	}
	if !g.PTT.Enabled || len(g.PTT.Channels) != 1 || g.PTT.Channels[0].Channel != 1 || g.PTT.Address != gsPTT.HostPort() {
		t.Fatalf("new group PTT = %+v", g.PTT)
	}
	grp := &g.ID
	if _, _, err := st.UpdatePhone(ctx, "test", c.Phone.ID, PhoneChange{GroupID: &grp}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetPhoneGroupMulticast(ctx, "test", g.ID, []phonebook.MulticastInput{{ChannelID: site.ID, Role: phonebook.RoleBoth}}, nil); !errors.As(err, &v) {
		t.Errorf("a PTT channel accepted as a group's paging channel: %v", err)
	}
	g, err = st.SetPhoneGroupMulticast(ctx, "test", g.ID, []phonebook.MulticastInput{{ChannelID: dock.ID, Role: phonebook.RoleSend}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Multicast) != 1 || g.Multicast[0].ID != dock.ID || g.Multicast[0].Role != phonebook.RoleSend {
		t.Fatalf("group multicast = %+v", g.Multicast)
	}
	if mc, _ := info(c.Phone.ID, "WP836"); mc == nil || len(mc.Listen) != 0 || len(mc.Send) != 1 {
		t.Errorf("group's send-only channel = %+v", mc)
	}
	if g.PagingOptions != phonebook.DefaultPagingOptions() {
		t.Errorf("new group paging options = %+v", g.PagingOptions)
	}
	opts := phonebook.PagingOptions{AllowInDND: true, Barge: 3, Timeout: 60, IntercomBarge: true}
	g, err = st.SetPhoneGroupMulticast(ctx, "test", g.ID, []phonebook.MulticastInput{{ChannelID: dock.ID, Role: phonebook.RoleSend}}, &opts)
	if err != nil {
		t.Fatal(err)
	}
	if g.PagingOptions != opts {
		t.Errorf("group paging options = %+v, want %+v", g.PagingOptions, opts)
	}
	if info(c.Phone.ID, "WP836"); *paging != (prov.PagingOptions{AllowInDND: 1, Barge: 3, Timeout: 60, IntercomBarge: 1}) {
		t.Errorf("provisioned paging options = %+v", *paging)
	}

	// Group paging on with no group channel defines the default group.
	set := phonebook.DefaultPTT()
	set.GroupPaging, set.DefaultGroup, set.PriorityChannel = true, 27, 20
	if g, err = st.SetPhoneGroupPTT(ctx, "test", g.ID, set); err != nil {
		t.Fatal(err)
	}
	if len(g.PTT.Channels) != 2 || g.PTT.Channels[1].Channel != 27 || !g.PTT.GroupPaging || g.PTT.PriorityChannel != 20 {
		t.Fatalf("group paging = %+v", g.PTT)
	}
	if _, ptt := info(c.Phone.ID, "WP856"); ptt == nil || !ptt.GroupPaging || ptt.DefaultGroup != 27 || ptt.Groups[1].Available != 1 ||
		ptt.Groups[1].Base != 82131 || ptt.PriorityChannel != 20 {
		t.Errorf("group paging provisioned as %+v", ptt)
	}
	if _, err := st.SetPhoneGroupPTT(ctx, "test", g.ID, phonebook.PTT{Enabled: false, DefaultChannel: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ptt := info(c.Phone.ID, "WP836"); ptt == nil || ptt.Enabled || ptt.GroupPaging {
		t.Errorf("group PTT off provisioned as %+v", ptt)
	}

	// A paging group sends to the channel: its kind stays, it is not deleted.
	pg, err := st.CreatePagingGroup(ctx, "test", PagingGroupInput{Name: "dock", Code: "*61", Priority: "normal", Enabled: true,
		Members: []int64{}, Callers: []int64{}, MulticastChannelID: &dock.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pg.MulticastChannel == nil || pg.MulticastChannel.Address != "239.255.0.1" {
		t.Fatalf("paging group channel = %+v", pg.MulticastChannel)
	}
	if _, err := st.CreatePagingGroup(ctx, "test", PagingGroupInput{Name: "ptt", Code: "*62", Priority: "normal", Enabled: true,
		Members: []int64{}, Callers: []int64{}, MulticastChannelID: &site.ID}, nil); !errors.As(err, &v) {
		t.Errorf("a paging group accepted a PTT channel: %v", err)
	}
	if _, err := st.UpdateMulticastChannel(ctx, "test", dock.ID, phonebook.ChannelInput{Label: "Dock", Address: "239.255.0.1", Port: 5004,
		Codec: phonebook.CodecPCMU, Priority: 1, Kind: phonebook.KindPTT}); !errors.Is(err, ErrInUse) {
		t.Errorf("kind change of a channel in use: %v", err)
	}
	if err := st.DeleteMulticastChannel(ctx, "test", dock.ID); !errors.Is(err, ErrInUse) {
		t.Errorf("delete of a paged channel: %v", err)
	}
	if err := st.DeleteMulticastChannel(ctx, "test", site.ID); err != nil {
		t.Fatal(err)
	}
	p, err := st.GetPhone(ctx, c.Phone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.PTTChannelID != nil {
		t.Errorf("deleted PTT channel still the phone's override: %v", *p.PTTChannelID)
	}
}
