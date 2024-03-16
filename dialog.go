package wsip

import (
	"github.com/emiago/sipgo/sip"
)

type Dialog struct {
	cltx *sip.ClientTx

	inviteFromUri sip.Uri
	inviteToUri   sip.Uri

	inviteSource      sip.Uri
	inviteDestination sip.Uri
}

func (d *Dialog) matchToUri(t sip.Uri) (sip.Uri, bool) {
	// Match only by user + host:port
	// TODO is this correct way?
	addr := t.Addr()
	if addr == d.inviteToUri.Addr() {
		return d.inviteDestination, true
	}

	if addr == d.inviteFromUri.Addr() {
		return d.inviteSource, true
	}

	return sip.Uri{}, false
}
