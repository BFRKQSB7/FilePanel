package nodes

import (
	"bytes"
	"context"
	"errors"
	"filepanel/internal/share"
	"testing"
)

func TestReplicasCopiesRoutesRemovalAndMoveToExistingCopy(t *testing.T) {
	h := fixture(t)
	a, b := h.peer(true), h.peer(true)
	data := bytes.Repeat([]byte("immutable replicated file"), 4096)
	v := h.file(nil, data)
	original, e := h.p.Shares.Get(v.ID)
	must(t, e)
	for _, target := range []string{a.n.Identity.ID, b.n.Identity.ID} {
		m, e := h.p.StartCopy(context.Background(), random(), v.ID, target)
		must(t, e)
		got := runTask(t, h, m)
		if got.State != "done" {
			t.Fatalf("copy: %+v", got)
		}
	}
	locs, e := h.p.Placements(v.ID)
	must(t, e)
	if len(locs) != 3 || !locs[0].Primary || locs[0].Node != "" {
		t.Fatalf("placements: %+v", locs)
	}
	for _, exit := range []string{"", a.n.Identity.ID, b.n.Identity.ID} {
		tr, e := h.p.NewDownload(context.Background(), v.ID, exit, "203.0.113.9", "", original.AuthVersion)
		must(t, e)
		if tr.Source != exit {
			t.Fatalf("exit-local replica not selected: %+v", tr)
		}
	}
	beforeUsage, e := a.n.Meter.Status()
	must(t, e)
	body, status := h.get(v, a, "")
	if status != 200 || !bytes.Equal(body, data) {
		t.Fatal("replica direct exit hash mismatch")
	}
	afterUsage, e := a.n.Meter.Status()
	must(t, e)
	if afterUsage.Period.Received != beforeUsage.Period.Received {
		t.Fatal("replica exit downloaded its own local copy through a network relay")
	}
	// Share revocation covers transfers issued from every replica.
	tr, e := h.p.NewDownload(context.Background(), v.ID, b.n.Identity.ID, "203.0.113.9", "", original.AuthVersion)
	must(t, e)
	must(t, h.p.Shares.Revoke(v.ID))
	if h.p.authorize(tr, true) == nil {
		t.Fatal("replica download survived share cancellation")
	}
	_, _, e = h.p.Shares.Update(v.ID, share.Input{Enabled: true})
	must(t, e)
	_, e = h.p.DB.DB.Exec("UPDATE node_transfer_leases SET until=0")
	must(t, e)
	m, e := h.p.StartMove(context.Background(), random(), v.ID, a.n.Identity.ID)
	must(t, e)
	got := runTask(t, h, m)
	if got.State != "done" {
		t.Fatalf("move to existing copy: %+v", got)
	}
	locs, e = h.p.Placements(v.ID)
	must(t, e)
	if len(locs) != 2 || locs[0].Node != a.n.Identity.ID {
		t.Fatalf("move lost other replicas: %+v", locs)
	}
	_, e = h.p.RemoveReplica(random(), v.ID, a.n.Identity.ID)
	if e == nil {
		t.Fatal("primary copy removal accepted")
	}
	m, e = h.p.RemoveReplica(random(), v.ID, b.n.Identity.ID)
	must(t, e)
	got = runTask(t, h, m)
	if got.State != "done" {
		t.Fatalf("remove: %+v", got)
	}
	locs, e = h.p.Placements(v.ID)
	must(t, e)
	if len(locs) != 1 {
		t.Fatalf("single-location mode retained copies: %+v", locs)
	}
	body, status = h.get(v, nil, "")
	if status != 200 || !bytes.Equal(body, data) {
		t.Fatal("last copy lost")
	}
}

func TestCopyFailureCancelAndRestartKeepOriginal(t *testing.T) {
	h := fixture(t)
	a := h.peer(true)
	v := h.file(nil, []byte("keep original and slug"))
	m, e := h.p.StartCopy(context.Background(), random(), v.ID, a.n.Identity.ID)
	must(t, e)
	h.p.MoveFault = func(stage string) error {
		if stage == "before_switch" {
			return errors.New("interrupted")
		}
		return nil
	}
	got := runTask(t, h, m)
	if got.Error == "" {
		t.Fatal("fault not exercised")
	}
	locs, e := h.p.Placements(v.ID)
	must(t, e)
	if len(locs) != 1 {
		t.Fatal("unverified copy published")
	}
	must(t, h.p.CancelMove(m.ID))
	h.p.MoveFault = nil
	got = runTask(t, h, m)
	if got.State != "cancelled" {
		t.Fatalf("cancel: %+v", got)
	}
	reopenMovePanel(t, h)
	locs, e = h.p.Placements(v.ID)
	must(t, e)
	if len(locs) != 1 {
		t.Fatal("cancel lost original")
	}
}

func TestReplicaFallbackRestartAndMigrationPreserveAllRemoteCopies(t *testing.T) {
	h := fixture(t)
	a, b := h.peer(true), h.peer(true)
	data := []byte("replica fallback after restart")
	v := h.file(a, data)
	for _, target := range []string{"", b.n.Identity.ID} {
		m, e := h.p.StartCopy(context.Background(), random(), v.ID, target)
		must(t, e)
		if got := runTask(t, h, m); got.State != "done" {
			t.Fatalf("copy: %+v", got)
		}
	}
	reopenMovePanel(t, h)
	_, e := h.p.DB.DB.Exec("UPDATE nodes SET last_seen=0 WHERE id=?", a.n.Identity.ID)
	must(t, e)
	body, status := h.get(v, nil, "")
	if status != 200 || !bytes.Equal(body, data) {
		t.Fatal("local verified replica did not replace offline primary source")
	}
	sh, _, e := h.p.Shares.Update(v.ID, share.Input{Enabled: true, PageTemplate: "showcase", RelayMode: "selected", Relays: []string{"local", b.n.Identity.ID}})
	must(t, e)
	archive, e := h.p.ExportMigration("replica-migration-password")
	must(t, e)
	next := fixture(t)
	_, e = next.p.ImportMigration(archive, "replica-migration-password", next.server.URL, next.ca, true)
	must(t, e)
	locs, e := next.p.Placements(v.ID)
	must(t, e)
	if len(locs) != 2 {
		t.Fatalf("remote replicas omitted from migration: %+v", locs)
	}
	restored, e := next.p.Shares.Get(v.ID)
	must(t, e)
	if restored.PageTemplate != "showcase" || restored.RelayMode != "selected" || len(restored.Relays) != 2 || restored.Slug != sh.Slug {
		t.Fatalf("share appearance or routes lost: %+v", restored)
	}
}
