package psip

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgox"
	"github.com/foxcpp/go-mockdns"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	register = `
REGISTER {{.Uri}} SIP/2.0
Via: SIP/2.0/{{.Transport}} {{.LocalIP}}:{{.LocalPort}};branch={{.Branch}}
Max-Forwards: 70
From: "sipp" <sip:[field0]@[field1]>;tag=[call_number]
To: "sipp" <sip:[field0]@[field1]>
Call-ID: reg///[call_id]
CSeq: 7 REGISTER
Contact: <sip:sipp@[local_ip]:[local_port]>
Expires: 3600
Content-Length: 0
User-Agent: SIPp
`
)

func TestIntegrationRegister(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Set TEST_INTEGRATION flag to run this test")
		return
	}

	addr := "127.1.1.1:6060"
	host, port, _ := sip.ParseAddr(addr)
	registry := NewRegistryMemory() // TODO change this with real database
	p := testProxySetup(t, "udp", "127.2.2.2:5060", "127.1.1.1:6060", WithProxyRegistry(registry))

	go p.s.ListenAndServe(context.TODO(), "udp", addr)
	// TODO remove this
	time.Sleep(1 * time.Second)

	ua, _ := sipgo.NewUA(sipgo.WithUserAgent("tester"))
	defer ua.Close()
	client, _ := sipgo.NewClient(ua)

	req := sip.NewRequest(sip.REGISTER, sip.Uri{User: "tester", Host: host, Port: port})
	req.AppendHeader(sip.NewHeader("Contact", "<sip:test@youwillnotfind.me>"))
	req.AppendHeader(sip.NewHeader("Expire", ""))

	res := testClientRequest(t, client, req)
	assert.Equal(t, sip.StatusOK, res.StatusCode, res.StartLine())

	// Check our registry
	bind, err := registry.GetRegisterBinding("tester")
	require.NoError(t, err)
	assert.Equal(t, "sip:tester@127.1.1.1", bind.Aor.String())
	assert.Equal(t, "sip:test@youwillnotfind.me", bind.Contacts[0].String())
}

func testProxy(ctx context.Context) error {
	ua, _ := sipgo.NewUA()
	defer ua.Close()

	return testProxyWithUA(ctx, ua,
		WithProxyInboundTarget(sip.Uri{Host: "127.0.0.200", Port: 5060}),
	)
}

func testProxyWithUA(ctx context.Context, ua *sipgo.UserAgent, options ...ProxyOption) error {
	// ua.TransportLayer().ConnectionReuse = false
	srv, _ := sipgo.NewServer(ua)
	defer srv.Close()
	p := NewProxy(
		srv,
		"",
		options...,
	)

	p.OnRequest = testProxyHandler
	return p.s.ListenAndServe(ctx, "udp4", "0.0.0.0:5099")
}

func testProxyHandler(rc *RequestContext) {
	log.Info().Msg("New request")
	req := rc.Request

	respCh, err := rc.RelayRequest(req)
	// err means some relay error, network error
	if err != nil {
		log.Error().Err(err).Msg("Fail to relay request")
		return
	}

	for res := range respCh {

		// Handle failure
		// switch res.StatusCode {
		// case 401, 403, 505, 600:
		// 	// Handle bad codes
		// 	req.SetDestination("failcarrier.com")
		// 	respCh, err := rc.RelayRequest(req)
		// 	// Check again ...
		// default:
		// 	// relay all

		// }
		rc.RelayResponse(res)
	}
}

func TestIntegrationSimpleCall(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Set TEST_INTEGRATION flag to run this test")
		return
	}

	// UAS Answer
	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("uas"))
		defer ua.Close()
		phone := sipgox.NewPhone(ua,
			sipgox.WithPhoneListenAddr(sipgox.ListenAddr{
				Network: "udp",
				Addr:    "127.0.0.200:5060",
			}),
			sipgox.WithPhoneLogger(log.With().Str("caller", "UAS").Logger()),
		)
		defer phone.Close()

		go func() {
			// Keep answering on calls
			// This will tear up and down
			dialog, err := phone.Answer(context.TODO(), sipgox.AnswerOptions{})
			require.NoError(t, err)
			t.Log("UAS answered")
			// answerDialog <- dialog
			<-dialog.Done()
			return

		}()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Proxy
	{
		go testProxy(ctx)
		time.Sleep(500 * time.Millisecond)
	}

	// UAC phone
	ua, _ := sipgo.NewUA(sipgo.WithUserAgent("uac"))
	defer ua.Close()

	phone := sipgox.NewPhone(ua,
		sipgox.WithPhoneLogger(log.With().Str("caller", "UAC").Logger()),
	)

	// Now dial
	ctx, _ = context.WithTimeout(ctx, 3*time.Second)
	dialog, err := phone.Dial(
		ctx,
		sip.Uri{Host: "0.0.0.0", Port: 5099},
		sipgox.DialOptions{})
	require.NoError(t, err)
	t.Log("UAC answered")
	defer dialog.Close()

	select {
	case <-dialog.Done():
	case <-time.After(1 * time.Second):
		dialog.Hangup(context.TODO())
	}
}

func TestIntegrationDNSDialogRouting(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Set TEST_INTEGRATION flag to run this test")
		return
	}

	// Here is test
	// We have 2 inboundtargets for proxy
	// loadbalancing is RR
	// all request inside dialog must land on same IP
	errc := make(chan error)
	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("uas"))
		defer ua.Close()
		phone := sipgox.NewPhone(ua,
			sipgox.WithPhoneListenAddr(sipgox.ListenAddr{
				Network: "udp",
				Addr:    "127.0.0.200:5066",
			}),
		)

		go func() {
			// Keep answering on calls
			// This will tear up and down
			dialog, err := phone.Answer(context.TODO(), sipgox.AnswerOptions{})
			if err != nil {
				errc <- err
				return
			}
			t.Log("UAS answered")
			// answerDialog <- dialog
			<-dialog.Done()
			return
		}()

		// Die imediatelly if phone is not ready
		select {
		case e := <-errc:
			require.NoError(t, e)
		case <-time.After(500 * time.Millisecond):
		}
	}

	{
		// Resolve all domains to this address
		dns, _ := mockdns.NewServerWithLogger(map[string]mockdns.Zone{
			"local200.": {
				A: []string{"127.0.0.200"},
			},
		}, &log.Logger, false)
		defer dns.Close()

		dns.PatchNet(net.DefaultResolver)
		defer mockdns.UnpatchNet(net.DefaultResolver)

		ua, _ := sipgo.NewUA(
			sipgo.WithUserAgentDNSResolver(net.DefaultResolver),
		)
		defer ua.Close()

		srv, _ := sipgo.NewServer(ua)
		defer srv.Close()

		p := NewProxy(
			srv,
			"localhost",
			WithProxyInboundTarget(sip.Uri{Host: "local200", Port: 5066}),
			WithProxyInboundTarget(sip.Uri{Host: "local100", Port: 5060}), // This one should never land
		)
		p.OnRequest = testProxyHandler

		go p.s.ListenAndServe(context.TODO(), "udp4", "0.0.0.0:5099")
		time.Sleep(500 * time.Millisecond)
	}

	// Create our dial phone
	ua, _ := sipgo.NewUA(sipgo.WithUserAgent("uac"))
	defer ua.Close()

	phone := sipgox.NewPhone(ua,
		sipgox.WithPhoneLogger(log.With().Str("caller", "UAC").Logger()),
	)

	// Dial
	ctx, _ := context.WithTimeout(context.Background(), 10*time.Second)
	dialog, err := phone.Dial(
		ctx,
		sip.Uri{Host: "0.0.0.0", Port: 5099},
		sipgox.DialOptions{})
	require.NoError(t, err)

	t.Log("UAC answered")
	select {
	case <-errc:
		require.NoError(t, err)
	case <-dialog.Done():
	case <-time.After(1 * time.Second):
		dialog.Hangup(context.TODO())
	}
}
