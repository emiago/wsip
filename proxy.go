package wsip

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Proxy is rfc implementation
// https://datatracker.ietf.org/doc/html/rfc3261#autoid-104

// proxy aka core
type Proxy struct {
	InboundTargets []sip.Uri
	OutboundTarget sip.Uri
	hostname       string

	s *sipgo.Server // Server transcaction
	c *sipgo.Client // Client transaction

	registry RegistryStore // Registry TODO: Move to interface

	Authentication string

	digestChallenge map[string]digest.Challenge

	dialogsMu sync.RWMutex
	dialogs   map[string]Dialog

	// Lets try reusing sipgo dialog managment
	// dialogCli *sipgo.DialogClient
	// dialogSrv *sipgo.DialogServer

	OnRequest func(rc *RequestContext)

	log zerolog.Logger
}

type ProxyOption func(p *Proxy)

func WithProxyRegistry(registry RegistryStore) ProxyOption {
	return func(p *Proxy) {
		p.registry = registry
	}
}

// WithProxyInboundTarget adds inbound target
// can be called multiple
func WithProxyInboundTarget(uri sip.Uri) ProxyOption {
	// TODO how to
	return func(p *Proxy) {
		p.InboundTargets = append(p.InboundTargets, uri)
	}
}

func WithProxyOutboundTarget(uri sip.Uri) ProxyOption {
	return func(p *Proxy) {
		p.OutboundTarget = uri
	}
}

func WithProxyLogger(logger zerolog.Logger) ProxyOption {
	return func(p *Proxy) {
		p.log = logger
	}
}

func NewProxy(srv *sipgo.Server, hostname string, options ...ProxyOption) *Proxy {

	p := &Proxy{
		hostname:        hostname,
		Authentication:  "digest",
		digestChallenge: make(map[string]digest.Challenge),
		registry:        NewRegistryMemory(),
		s:               srv,
		dialogs:         make(map[string]Dialog),
		log:             log.With().Str("caller", "proxy").Logger(),
	}

	for _, o := range options {
		o(p)
	}

	client, err := sipgo.NewClient(srv.UserAgent,
		sipgo.WithClientHostname(hostname),
	) // sipgo.WithClientHostname("localhost"),
	// sipgo.WithClientAddr(p.uri.Addr()),

	// TODO return it
	if err != nil {
		log.Fatal().Err(err).Msg("Fail to setup client handle")
	}
	p.c = client

	p.setupHandlers()

	return p
}

func (p *Proxy) setupHandlers() {
	srv := p.s

	allRoute := p.requestContext

	srv.OnRegister(p.registerHandler)
	srv.OnInvite(allRoute)
	srv.OnAck(p.ackHandler)
	// srv.OnCancel(allRoute)
	srv.OnBye(allRoute)
	srv.OnRefer(allRoute)

	srv.OnOptions(p.optionsHandler)
}

// Are we acting as registrar or proxy

// https://datatracker.ietf.org/doc/html/rfc3261#section-16.3
func (p *Proxy) validationMiddleware(next sipgo.RequestHandler) sipgo.RequestHandler {
	return func(req *sip.Request, tx sip.ServerTransaction) {
		// TODO
		next(req, tx)
	}
}

func (p *Proxy) requestContext(req *sip.Request, tx sip.ServerTransaction) {
	rc := &RequestContext{
		Request: req,
		stx:     tx,
		p:       p,
		log: log.With().
			Str("caller", fmt.Sprintf("proxy-request<%s>", req.Method.String())).
			Str("call-id", req.CallID().Value()).
			Logger(),
	}

	p.OnRequest(rc)
}

// https://datatracker.ietf.org/doc/html/rfc3261#section-10
func (p *Proxy) registerHandler(req *sip.Request, tx sip.ServerTransaction) {
	// TODO rfc5626
	// https://datatracker.ietf.org/doc/html/rfc5626#section-3.2
	// This is when we have contact header
	// <sip:line1@192.0.2.2;transport=tcp>; reg-id=1;
	// ;+sip.instance="<urn:uuid:00000000-0000-1000-8000-000A95A0E128>"
	// end we can use this reg-id and uuid to replace existing Contact

	// 1. inspect request uri
	if req.Recipient.Host != p.hostname {
		tx.Respond(sip.NewResponseFromRequest(req, 401, "Incorrect sip domain", nil))
		return
	}

	// 2 Require field
	// TODO

	// 3. Authentification
	// TODO Move this as middlware
	switch p.Authentication {
	case "digest":
		res, err := p.digestAuth(req)
		if err != nil {
			log.Error().Err(err).Msg("fail to authenticate")
			tx.Respond(res)
			return
		}
	}

	// 5 extracting To AOR
	to := req.To()
	if to == nil {
		tx.Respond(sip.NewResponseFromRequest(req, 404, "Missing TO header", nil))
		return
	}
	aor := to.Address
	// Validate AOR?

	// 6. Check Contact header

	expiry := 60 * time.Minute // TODO: make this configurable
	if h := req.GetHeader("Expires"); h != nil {
		var err error
		expiry, err = time.ParseDuration(h.Value() + "s")
		if err != nil {
			tx.Respond(sip.NewResponseFromRequest(req, 400, "Expires header malformed", nil))
			return
		}
	}

	// TODO check some minimal interval allowance and set Min-Expires header

	hdrs, err := ReadHeaderByType[*sip.ContactHeader](req, "Contact")
	if err != nil {
		log.Error().Err(err).Msg("Fail to read contact headers")
		tx.Respond(sip.NewResponseFromRequest(req, 500, "Server internal error", nil))
		return
	}

	// 7. Processing each contact header
	contacts := make([]sip.Uri, 0, len(hdrs))

	// Are we removing DEREGISTERING?
	// https://datatracker.ietf.org/doc/html/rfc3261#section-10.2.2
	if expiry == 0 && len(hdrs) == 1 && hdrs[0].DisplayName == "*" {
		p.registry.DeleteRegisterBinding(aor.User)
		tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		return

	}

	// Check does binding exists
	// storedContacts := p.store.GetAORContacts(aor.User)
	for _, h := range hdrs {

		/*  If the address-of-record in the To header field of a REGISTER request
		is a SIPS URI, then any Contact header field values in the request
		SHOULD also be SIPS URIs.  Clients should only register non-SIPS URIs
		under a SIPS address-of-record when the security of the resource
		represented by the contact address is guaranteed by other means. */

		// TODO Check prioritization
		// https://datatracker.ietf.org/doc/html/rfc3261#section-10.2.1.2

		expires := h.Params["expires"]

		// Are we removing DEREGISTERING?
		// https://datatracker.ietf.org/doc/html/rfc3261#section-10.2.2
		if expires == "0" {
			continue
		}
		// if expires := h.Params["expires"]; expires != "" {
		// 	// TODO
		// }

		// // Check bindings
		// var found sip.Uri
		// for _, c := range storedContacts {
		// 	if c.String() == h.String() {
		// 		found = c
		// 	}
		// }

		contacts = append(contacts, h.Address)
	}

	// 8. Returning 200

	callid := req.CallID()
	// TODO compare callid

	binding := RegisterBinding{
		Aor:      aor,
		CallID:   *callid,
		Contacts: contacts,
		Expiry:   expiry,
	}

	if err := p.registry.CreateRegisterBinding(binding); err != nil {
		reply(tx, req, 500, "Internal server error")
		return
	}
	// Each Contact value MUST feature an "expires"
	//  parameter indicating its expiration interval chosen by the
	//  registrar.  The response SHOULD include a Date header field.

	res := sip.NewResponseFromRequest(req, 200, "OK", nil)

	for _, h := range hdrs {
		ch := h.Clone()
		ch.Params["expires"] = fmt.Sprintf("%d", int(expiry.Seconds()))
		res.AppendHeader(ch)
	}

	log.Info().Str("aor", aor.String()).Str("contact", binding.Contacts[0].String()).Msg("Registered")

	tx.Respond(res)

}

func (p *Proxy) ackHandler(req *sip.Request, tx sip.ServerTransaction) {
	dstUri := p.getDestination(req)
	if dstUri.Host == "" {
		return
	}
	req.SetDestination(dstUri.HostPort())
	// req.Recipient = &dst
	// req.Recipient = &sip.Uri{User: dstUri.User, Host: dstUri.Host, Port: dstUri.Port}

	if err := p.c.WriteRequest(req, sipgo.ClientRequestAddVia); err != nil {
		log.Error().Err(err).Msg("Send failed")
		reply(tx, req, 500, "")
	}
}

func (p *Proxy) optionsHandler(req *sip.Request, tx sip.ServerTransaction) {
	// https://datatracker.ietf.org/doc/html/rfc3261#autoid-69

	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
	// NOTE: If the response is generated by a proxy, the Allow header
	// field SHOULD be omitted as it is ambiguous since a proxy is method
	// agnostic.

	methods := p.s.RegisteredMethods()
	res.AppendHeader(sip.NewHeader("Allow", strings.Join(methods, ",")))
	res.AppendHeader(sip.NewHeader("Accept", "application/sdp"))
	res.AppendHeader(sip.NewHeader("Accept-Encoding", "")) // TODO how to support encoding
	res.AppendHeader(sip.NewHeader("Accept-Language", "en"))
	res.AppendHeader(sip.NewHeader("Supported", ""))
}

// returns 200 response or error with non 200 response
func (p *Proxy) digestAuth(req *sip.Request) (res *sip.Response, err error) {
	h := req.GetHeader("Authorization")
	if h == nil {
		cha := digest.Challenge{
			Realm:     "sipgo-server",
			Nonce:     fmt.Sprintf("%d", time.Now().UnixMicro()),
			Opaque:    "sipgo",
			Algorithm: "MD5",
		}

		// Add to our list
		p.digestChallenge[cha.Nonce] = cha

		// TODO check how to cleanup challenges
		time.AfterFunc(10*time.Second, func() {
			delete(p.digestChallenge, cha.Nonce)
		})

		res := sip.NewResponseFromRequest(req, 401, "Unathorized", nil)
		res.AppendHeader(sip.NewHeader("WWW-Authenticate", cha.String()))

		return res, nil
	}

	cred, err := digest.ParseCredentials(h.Value())
	if err != nil {
		return sip.NewResponseFromRequest(req, 401, "Bad credentials", nil), err
	}

	// Check registry
	rec, err := p.registry.GetRegisterBinding(cred.Username)
	if err != nil {
		return sip.NewResponseFromRequest(req, 404, "Bad authorization header", nil), err
	}
	aor := rec.Aor

	// Get our challenge
	cha, exists := p.digestChallenge[cred.Nonce]
	if !exists {
		return sip.NewResponseFromRequest(req, 401, "Challenge expired", nil), err
	}

	// Make digest and compare response
	digCred, err := digest.Digest(&cha, digest.Options{
		Method:   req.Method.String(),
		URI:      cred.URI,
		Username: cred.Username,
		Password: aor.Password,
	})

	if err != nil {
		return sip.NewResponseFromRequest(req, 401, "Bad credentials", nil), err
	}

	if cred.Response != digCred.Response {
		return sip.NewResponseFromRequest(req, 401, "Unathorized", nil), fmt.Errorf("non matching creds")
	}

	// TODO check our credentials accounts

	return sip.NewResponseFromRequest(req, 200, "OK", nil), nil
}

var rrcounter int // Loadbalance counter
func (p *Proxy) getDestination(req *sip.Request) sip.Uri {
	tohead := req.To()

	// Withing dialog routing
	if tag, _ := tohead.Params.Get("tag"); tag != "" {
		did, _ := sip.MakeDialogIDFromRequest(req)
		p.dialogsMu.RLock()
		d, exists := p.dialogs[did]
		p.dialogsMu.RUnlock()
		if exists {
			targetUri, matched := d.matchToUri(tohead.Address)
			if matched {
				return targetUri
			}
			return sip.Uri{}
			// TODO: Should we fallback to registration?
		}
	}

	rec, err := p.registry.GetRegisterBinding(tohead.Address.User)
	if err != nil || err == ErrRegistryDoesNotExist {

		for _, uri := range p.InboundTargets {
			switch {
			// This does not work for DNS names
			case req.Source() == uri.HostPort():
				return p.OutboundTarget
			}
		}

		// Lets load balance
		dst := p.InboundTargets[rrcounter%len(p.InboundTargets)]
		rrcounter++
		return dst
	}

	for _, c := range rec.Contacts {
		return c
	}

	return rec.Aor
}

func (p *Proxy) createInviteDialog(res *sip.Response) {

}

func reply(tx sip.ServerTransaction, req *sip.Request, code sip.StatusCode, reason string) {
	resp := sip.NewResponseFromRequest(req, code, reason, nil)
	resp.SetDestination(req.Source()) //This is optional, but can make sure not wrong via is read
	if err := tx.Respond(resp); err != nil {
		log.Error().Err(err).Msg("Fail to respond on transaction")
	}
}
