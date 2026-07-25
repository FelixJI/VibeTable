package launch

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/buildinfo"
)

func TestOpenLoopbackUsesKernelAssignedIPv4Port(t *testing.T) {
	listener, err := OpenLoopback()
	if err != nil {
		t.Fatalf("OpenLoopback(): %v", err)
	}
	defer listener.Close()

	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" || port == "" || port == "0" {
		t.Fatalf("listener address = %q", listener.Addr())
	}
}

func TestOpenLoopbackRejectsInvalidPort(t *testing.T) {
	if _, err := openLoopback("127.0.0.1:not-a-port"); err == nil {
		t.Fatal("openLoopback() unexpectedly succeeded")
	}
}

func TestAnnounceOnFirstAcceptRunsBeforeAcceptBlocksAndOnlyOnce(t *testing.T) {
	raw, err := OpenLoopback()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	announced := make(chan string, 1)
	listener, err := AnnounceOnFirstAccept(raw, func(address string) error {
		announced <- address
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	accepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		accepted <- connection
	}()

	select {
	case address := <-announced:
		if address != raw.Addr().String() {
			t.Fatalf("announced address = %q, want %q", address, raw.Addr())
		}
	case <-time.After(time.Second):
		t.Fatal("ready announcement did not run before Accept blocked")
	}

	client, err := net.DialTimeout("tcp4", raw.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	if server == nil {
		t.Fatal("listener did not accept connection")
	}
	server.Close()

	secondAccepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		secondAccepted <- connection
	}()
	secondClient, err := net.DialTimeout("tcp4", raw.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer secondClient.Close()
	secondServer := <-secondAccepted
	if secondServer == nil {
		t.Fatal("listener did not accept second connection")
	}
	secondServer.Close()
	if len(announced) != 0 {
		t.Fatal("ready announcement ran more than once")
	}
}

func TestAnnounceOnFirstAcceptPropagatesAnnouncementFailure(t *testing.T) {
	raw, err := OpenLoopback()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := AnnounceOnFirstAccept(raw, func(string) error {
		return errors.New("write failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listener.Accept(); err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("Accept() error = %v", err)
	}
}

func TestWriteReadyIsMachineReadableAndContainsNoSecretField(t *testing.T) {
	var output bytes.Buffer
	ready := ReadyRecord("127.0.0.1:49152", buildinfo.Current("hash"))
	if err := WriteReady(&output, ready); err != nil {
		t.Fatalf("WriteReady(): %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("ready JSON: %v", err)
	}
	if decoded["contract"] != ReadyContract {
		t.Fatalf("contract = %v", decoded["contract"])
	}
	if strings.Contains(strings.ToLower(output.String()), "secret") {
		t.Fatalf("ready record leaked a secret field: %s", output.String())
	}
}

func TestWriteReadyRejectsUnassignedOrNonLoopbackAddress(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1:0",
		"127.0.0.1:not-a-port",
		"127.0.0.1:65536",
		"0.0.0.0:49152",
		"localhost:49152",
		"bad",
	} {
		if err := WriteReady(&bytes.Buffer{}, ReadyRecord(address, buildinfo.Current("hash"))); err == nil {
			t.Fatalf("WriteReady(%q) unexpectedly succeeded", address)
		}
	}
}
