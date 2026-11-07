package wait

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The timeout message used to say `cloud app get` whatever was being waited
// on; a VM or database wait must name its own command.
func TestUntil_TimeoutNamesTheCheckCommand(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	_, err := Until(context.Background(), sequence("provisioning"), nil,
		opts(c, Options{Timeout: 10 * time.Second, CheckCommand: "cloud vm get web"}))
	if err == nil || !strings.Contains(err.Error(), "`cloud vm get web`") || strings.Contains(err.Error(), "cloud app get") {
		t.Errorf("message must name the VM command: %v", err)
	}
	_, err = Until(context.Background(), sequence("provisioning"), nil, opts(c, Options{Timeout: 10 * time.Second}))
	if err == nil || strings.Contains(err.Error(), "cloud app get") {
		t.Errorf("without a command the message must stay generic: %v", err)
	}
}

func TestErrFailed_StripsControlCharacters(t *testing.T) {
	msg := (&ErrFailed{Status: "failed\x1b[2J", Reason: "boom\x1b]0;x\x07"}).Error()
	if strings.ContainsAny(msg, "\x1b\x07") {
		t.Errorf("control characters survived: %q", msg)
	}
}
