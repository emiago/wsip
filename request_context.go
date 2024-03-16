package wsip

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/rs/zerolog"
)

type RequestContext struct {
	Request *sip.Request
	stx     sip.ServerTransaction
	p       *Proxy

	// Additional
	dialog *DialogMutex
	log    zerolog.Logger
}

// SetLogger can be used to change default logger before any relaying or other action on request
func (rc *RequestContext) SetLogger(log zerolog.Logger) {
	rc.log = log
}

func (rc *RequestContext) Close() {
	rc.stx.Terminate()
}

func (rc *RequestContext) Dialog() *DialogMutex {
	return rc.dialog
}

func (rc *RequestContext) Topos(req *sip.Request) *sip.Request {
	// TODO
	return req
}

var (
	ErrRelayNotFound = errors.New("Destination not found")
)

func (rc *RequestContext) RelayRequest(req *sip.Request) (<-chan *sip.Response, error) {
	p := rc.p
	tx := rc.stx

	// Inherits from proxy
	// If we are proxying to asterisk or other proxy -dst must be set
	// Otherwise we will look on our registration entries

	// In case registration qvalue must be used to process highest to lowest
	// https://datatracker.ietf.org/doc/html/rfc3261#section-16.6
	dstUri := p.getDestination(req)

	if dstUri.Host == "" {
		return nil, ErrRelayNotFound
	}

	dst := dstUri.HostPort()
	rc.log.Debug().Str("dst", dst).Msg("Get destination")

	// Example of ROUTE and Record Route headers procesing
	// https://datatracker.ietf.org/doc/html/rfc3261#section-16.12.1.2

	// Simple explanation:
	// If request contains Route header and next hop
	// - Does not contain ;lr -> strict routing
	//						  -> Then replace request URI with Route and add current RequestURI as last RouteHeader
	// 						  -> last RouteHeader must be used by loose router to return back to request URI if finds a match

	// - Does contain ;lr -> loose routing -> Do not change REquestURI and just remove Route header

	// Before setting destination
	// TODO strict routing
	// https://datatracker.ietf.org/doc/html/rfc3261#section-16.4
	// 	The proxy MUST inspect the Request-URI of the request.  If the
	//    Request-URI of the request contains a value this proxy previously
	//    placed into a Record-Route header field (see Section 16.6 item 4),
	//    the proxy MUST replace the Request-URI in the request with the last
	//    value from the Route header field, and remove that value from the
	//    Route header field.

	// https://datatracker.ietf.org/doc/html/rfc3261#section-16.6
	//	If the copy contains a Route header field, the proxy MUST
	// 	inspect the URI in its first value.  If that URI does not
	// 	contain an lr parameter, the proxy MUST modify the copy as
	// 	follows:

	// 	-  The proxy MUST place the Request-URI into the Route header
	// 	   field as the last value.

	// 	-  The proxy MUST then place the first Route header field value
	// 	   into the Request-URI and remove that value from the Route
	// 	   header field.

	// 	Proxy Processing (Strict Routing):
	// The proxy examines the Route headers and forwards the request according to the strict routing rules.
	// It ignores any Record-Route headers in the request.
	// If no Route headers are present, the proxy forwards the request based on its routing logic.

	// 	Proxy Processing (Loose Routing):
	// If loose routing is allowed, the proxy checks the Route headers but prefers the Request-URI for routing if no Route headers are present.
	// It may also consider Record-Route headers for routing decisions.
	// The proxy may insert its own Route header if necessary.

	// TODO when to actually set destination?
	// We will override ROUTE header check with this command
	req.SetDestination(dst)

	// Rewrite Contact to prevent some external
	// if contact, exists := req.Contact(); exists {
	// 	contact.Address =
	// }

	// 	1.  Make a copy of the received request

	// 	2.  Update the Request-URI
	// req.Recipient = &sip.Uri{User: dstUri.User, Host: dstUri.Host, Port: dstUri.Port}

	// 	3.  Update the Max-Forwards header field

	// 	4.  Optionally add a Record-route header field value
	// 	5.  Optionally add additional header fields

	// 	6.  Postprocess routing information

	// 	7.  Determine the next-hop address, port, and transport

	// 	8.  Add a Via header field value

	// 	9.  Add a Content-Length header field if necessary

	// 	10. Forward the new request

	// 	11. Set timer C

	/*

	   Set timer C

	            In order to handle the case where an INVITE request never
	            generates a final response, the TU uses a timer which is called
	            timer C.  Timer C MUST be set for each client transaction when
	            an INVITE request is proxied.  The timer MUST be larger than 3
	            minutes.  Section 16.7 bullet 2 discusses how this timer is
	            updated with provisional responses, and Section 16.8 discusses
	            processing when it fires.
	*/

	// Start client transaction and relay our request
	// TODO this could be some sipgo.TransactionRelay

	// 1. Find the appropriate response context
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rc.log.Debug().Str("res", req.StartLine()).Msg("Relay request")
	clTx, err := p.c.TransactionRequest(ctx, req, sipgo.ClientRequestAddVia, sipgo.ClientRequestAddRecordRoute)
	if err != nil {
		// reply(tx, req, 500, "")
		return nil, err
	}

	// Keep monitoring transactions, and proxy client responses to server transaction
	rc.log.Debug().Str("req", req.Method.String()).Msg("Starting transaction")
	respChan := make(chan *sip.Response)
	go func() {
		defer clTx.Terminate()
		defer close(respChan)
		for {
			select {

			// TODO
			// If there are no final responses in the context, the proxy MUST
			// send a 408 (Request Timeout) response to the server
			// transaction.
			// case <-time.After(60 * time.Second):

			case res, more := <-clTx.Responses():
				if !more {
					// close(respChan)
					return
				}

				/*
					TODO is this handled?
					Update timer C for provisional responses

					For an INVITE transaction, if the response is a provisional
					response with status codes 101 to 199 inclusive (i.e., anything
					but 100), the proxy MUST reset timer C for that client
					transaction.  The timer MAY be reset to a different value, but
					this value MUST be greater than 3 minutes.
				*/

				if res.StatusCode == sip.StatusOK && req.Method == sip.INVITE {
					// TODO thread safe
					id, err := sip.MakeDialogIDFromResponse(res)
					if err != nil {
						rc.log.Error().Err(err).Msg("Failed to create dialog")
					} else {
						rc.log.Info().Str("source", req.Source()).Str("dst", dst).Msg("New dialog")

						origFrom := req.From()
						origTo := req.To()
						// This address are IP (DNS resolved) address
						// Consider that req.Destination is changed to IP if resolved by address
						// so this way we have DNS resolving once
						shost, sport, _ := sip.ParseAddr(req.Source())
						dhost, dport, _ := sip.ParseAddr(req.Destination())

						// TODO we need consider Contact header and use that to cache in DNS!
						p.dialogsMu.Lock()
						p.dialogs[id] = Dialog{
							inviteFromUri: origFrom.Address,
							inviteSource:  sip.Uri{Host: shost, Port: sport},

							inviteToUri:       origTo.Address,
							inviteDestination: sip.Uri{Host: dhost, Port: dport},
						}
						p.dialogsMu.Unlock()
					}
				}

				// 3. Remove topmost response via
				res.RemoveHeader("Via")

				// 4. TODO 3xx response

				// 5. Check response for forwarding

				// TODO 6xx response

				// 	A stateful proxy MUST NOT immediately forward any other
				//  responses.  In particular, a stateful proxy MUST NOT forward
				//  any 100 (Trying) response.  Those responses that are candidates
				//  for forwarding later as the "best" response have been gathered
				//  as described in step "Add Response to Context".

				// 6. Sending final response

				// 7. Aggregate Authorization Header Field Values

				// 8. TODO

				// 9.

				// Handle NAT
				res.RemoveHeader("Contact")
				res.AppendHeader(&sip.ContactHeader{
					Address: sip.Uri{
						User: p.s.Name(),
						Host: p.hostname,
						Port: p.s.TransportLayer().GetListenPort(sip.NetworkToLower(req.Transport())),
					},
					Params: sip.NewParams(),
				})

				res.SetDestination(req.Source())
				// rc.log.Info().Str("dst", req.Source()).Msg("relaying response")
				// if err := tx.Respond(res); err != nil {
				// 	rc.log.Error().Err(err).Msg("ResponseHandler transaction respond failed")
				// }

				rc.log.Debug().Str("res", res.StartLine()).Msg("Getting response")
				select {
				case <-tx.Done():
				case respChan <- res:
				}

			case m := <-tx.Acks():
				// Acks can not be send directly trough destination
				rc.log.Info().Str("m", m.StartLine()).Str("dst", dst).Msg("Proxing ACK")
				m.SetDestination(dst)
				p.c.WriteRequest(m)

			case m := <-tx.Cancels():
				// Send response imediatelly
				reply(tx, m, 200, "OK")
				// Cancel client transacaction without waiting. This will send CANCEL request
				clTx.Cancel()

			case <-tx.Done():
				if err := tx.Err(); err != nil {
					rc.log.Error().Err(err).Str("req", req.Method.String()).Msg("Transaction done with error")
					return
				}
				rc.log.Debug().Str("req", req.Method.String()).Msg("Transaction done")
				return
			}
		}
	}()

	return respChan, nil
}

func (rc *RequestContext) RelayResponse(res *sip.Response) error {
	// p := rc.p
	tx := rc.stx
	// resp.SetDestination(req.Source()) //This is optional, but can make sure not wrong via is read
	rc.log.Debug().Str("res", res.StartLine()).Msg("Relay response")
	if err := tx.Respond(res); err != nil {
		// rc.log.Error().Err(err).Msg("Fail to respond on transaction")
		return err
	}
	return nil
}

type DialogMutex struct {
	d  Dialog
	mu sync.RWMutex
}
