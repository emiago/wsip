package wsip

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgox"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

func TestProxyE2ECall(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Set TEST_INTEGRATION flag to run this test")
		return
	}

	// UAS Answer
	go func() {
		ua, _ := sipgo.NewUA(
			sipgo.WithUserAgent("uas"),
		)
		phone := sipgox.NewPhone(ua,
			sipgox.WithPhoneListenAddr(sipgox.ListenAddr{
				Network: "udp",
				Addr:    "127.0.0.200:5060",
			}),
			sipgox.WithPhoneLogger(log.With().Str("caller", "UAS").Logger()),
		)

		// Wait for answer
		dialog, err := phone.Answer(context.TODO(), sipgox.AnswerOptions{})
		require.NoError(t, err)

		// Wait for call/dialog to be completed
		t.Log("UAS answered")
		<-dialog.Done()
		return

	}()

	// Proxy
	cmd := exec.Command("go", "run", "./cmd/wsip", "-in", "127.0.0.200:5060")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logreader, _ := cmd.StdoutPipe()
	defer logreader.Close()

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := logreader.Read(buf)
			if err != nil {
				return
			}
			fmt.Printf("wsip: %s", string(buf[:n]))
		}
	}()
	err := cmd.Start()
	require.NoError(t, err)

	defer cmd.Wait()
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	time.Sleep(500 * time.Millisecond)

	// UAC phone
	ua, _ := sipgo.NewUA(
		sipgo.WithUserAgent("uac"),
	)
	phone := sipgox.NewPhone(ua,
		sipgox.WithPhoneLogger(log.With().Str("caller", "UAC").Logger()),
	)

	// Now dial
	ctx, _ := context.WithTimeout(context.Background(), 3*time.Second)
	dialog, err := phone.Dial(
		ctx,
		sip.Uri{Host: "127.0.0.1", Port: 5060},
		sipgox.DialOptions{})
	require.NoError(t, err)
	t.Log("UAC answered")

	select {
	case <-dialog.Done():
	case <-time.After(1 * time.Second):
		dialog.Hangup(context.TODO())
	}
}
