# Wiresip (WIP)

Wiresip is GO SIP library that hides some complexity of building SIP stateful proxies.

Inspiration for this is that current some solutions are hard to customize but also hard to test changes.

Main goal is that you are able to relaying request responses but under control and all 
that with simple API.

Here are some features path so that you know where this project is heading:
- [x] Defining (inbound) targets and relaying request
- [x] Dialog and cached DNS destinations
- [x] Built in memory register handling
- [x] Proxy dialog (call) routing works
- [ ] Create `cli` single binary based proxy for easy running as POC
- [ ] Unit testing handler
- [ ] Matching Inbound or Outbound targets based on ip ranges, prefix numbers etc..
- [ ] Extend dialog with current `sipgo.Dialog`
- [ ] Dialog external caching interface for HA and scaling
- [ ] IP auth module based on request Source
- [ ] HA solutions with external dialog/transaction storage (`sipgo` patching)
- [ ] Digest auth out of box 
- [ ] Access dialogs and monitoring
- [ ] RTP Proxy and media gateway
- [ ] Topos module like kamailio for full topology hidding

## Controling request and relay

Here is example of building proxy with several lines of code

```go 
ua, _ := sipgo.NewUA()
srv, _ := sipgo.NewServer(ua)
p := NewProxy(
    srv,
    "", // hostname
    WithProxyInboundTarget(sip.Uri{Host: "127.0.0.200", Port: 5060}),
)
go p.s.ListenAndServe(ctx, "udp", "127.0.0.1:5060")
go p.s.ListenAndServe(ctx, "tcp", "127.0.0.1:5060")

proxy.OnRequest(func(rc RequestContext) {
    // RelayRequest will:
    // - Check registrar 
    // - Detect call direction. Src IP is compared to Inbound(internal) IP Targets ranges
    // - Relay reqeust
    // req.SetDestination() will skip above checking
    respCh, err := rc.RelayRequest(req)
    if err != nil { /*log error*/return}

    // Keep relaying responses until it gets closed
    for res := range respCH {
        // Handle failure
        switch res.StatusCode {
            case 401, 403, 505, 600:
                // Handle bad codes
                req.SetDestination("failcarrier.com")
                respCh, err := rc.RelayRequest(req)
                // Check again ...
            default:
                // relay all

        }
        rc.RelayResponse(res)
    }
})
```
## Inbound Outbound targets (TODO)

More granual control of defining IP.

```go
WithProxyInboundTarget(
    sip.Uri{Host: "my.asterisk.xy", Port: 5060},
    // MatchIP should be defined in case URI is host name, otherwise it will be DNS resolved each time
    wsip.MatchIP( 
        "10.1.1.0/24", // With range ips to match in case this is dynamic
        "10.2.2.1", // With static ips
    ),
)
```

```go
WithProxyOutboundTarget(
    sip.Uri{Host: "sip.carrier.com", Port: 5060},
    wsip.ToPrefix("49"), // or gsip.ToRegex("^49(1-2)"),
    0.5 // In case multiple matching carrier  < 1.0 request is loadbalanced
)
```
## RelayRequest (WIP)

`RelayRequest` flow:
- If no destination is set with `req.SetDestination` then
- Check registrar if used as registrar
- Detect call direction Src IP is compared to Inbound `MatchIP`
- Filter all with matching Destination via Prefix, Regex
- Load balance unless target has weight 1.0
- Relay reqeust


`RelayRequest` internally:
- creates client transaction 
- sends request and returns all responses
- responses can be manipulated but mostly they should be relayed to originator
- IN case 200 response for INVITE it creates dialog which can be accessed via `rc.Dialog()`


### RTP proxy (IDEA)

RTP Proxy signaling:
- Reads INVITE SDP
- Setups RTP/RTCP ports for proxy external and internal interface (interface call as it can be seperate Service)
- Changes to local interface address where our proxy 172.17.0.0/12
- Applies new SDP with internal RTP/RTCP ports
- Relays request
- Reads Response (only 200)
- Changes SDP to external interface 
- Changes SDP port to external PORTs
- Returns response
- When `RequestContext` dies RTP/RTCP will be closed



# How to unit test request/response

This requires simulating UAC and UAS and testing our `OnRequest`  handler

TODO




