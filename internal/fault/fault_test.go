package fault

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(n int) *int           { return &n }
func floatPtr(f float64) *float64 { return &f }
func bytePtr(b uint8) *Byte       { v := Byte(b); return &v }

func mustAdd(t *testing.T, s *Store, f Fault) Fault {
	t.Helper()
	added, err := s.Add(f)
	require.NoError(t, err)
	return added
}

func TestAdd_ValidatesAndFillsDefaults(t *testing.T) {
	s := NewStore()
	f := mustAdd(t, s, Fault{Kind: RedfishError})
	assert.Equal(t, "1", f.ID)
	assert.Equal(t, "redfish", f.Target)
	assert.Equal(t, 500, f.Status)

	f = mustAdd(t, s, Fault{Kind: RedfishHang})
	assert.Equal(t, DefaultHangSeconds, f.HangSeconds)
	f = mustAdd(t, s, Fault{Kind: IPMIError})
	assert.Equal(t, Byte(0xFF), *f.CompletionCode)

	for _, bad := range []Fault{
		{Kind: "nope"},
		{Kind: RedfishDelay},
		{Kind: RedfishError, Status: 200},
		{Kind: IPMIDrop, Target: "redfish"},
		{Kind: IPMIDrop, Count: intPtr(0)},
		{Kind: IPMIDrop, DurationSeconds: floatPtr(-1)},
		{Kind: RedfishDrop, Path: "["},
	} {
		_, err := s.Add(bad)
		assert.Error(t, err, "%+v", bad)
	}
}

func TestParse(t *testing.T) {
	faults, err := Parse(`[{"kind":"ipmi-error","netfn":"0x00","cmd":2,"completionCode":"0xc0","count":1},{"kind":"redfish-drop"}]`)
	require.NoError(t, err)
	require.Len(t, faults, 2)
	assert.Equal(t, Byte(0x00), *faults[0].NetFn)
	assert.Equal(t, Byte(0x02), *faults[0].Cmd)
	assert.Equal(t, Byte(0xC0), *faults[0].CompletionCode)
	assert.Equal(t, 1, *faults[0].Count)

	_, err = Parse(`[{"kind":"ipmi-drop","netfn":256}]`)
	assert.Error(t, err)
	_, err = Parse(`[{"kind":"ipmi-drop","bogus":1}]`)
	assert.Error(t, err)
}

func TestCount_RemovesFaultWhenUsedUp(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, Fault{Kind: PowerActionIgnored, Count: intPtr(2)})
	calls := 0
	reset := func(string) error { calls++; return nil }

	require.NoError(t, s.Reset("On", reset))
	list := s.List()
	require.Len(t, list, 1)
	assert.Equal(t, 1, *list[0].Count)
	assert.Equal(t, 1, list[0].Applied)
	require.NoError(t, s.Reset("On", reset))
	assert.Empty(t, s.List())
	assert.Equal(t, 0, calls)

	require.NoError(t, s.Reset("On", reset))
	assert.Equal(t, 1, calls)
}

func TestDuration_ExpiresFault(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	f := mustAdd(t, s, Fault{Kind: IPMIDrop, DurationSeconds: floatPtr(10)})
	require.NotNil(t, f.ExpiresAt)

	assert.True(t, s.IPMI(IPMIRequest{HasCommand: true}).Drop)
	now = now.Add(10 * time.Second)
	assert.False(t, s.IPMI(IPMIRequest{HasCommand: true}).Drop)
	assert.Empty(t, s.List())
}

func TestDeleteAndClear(t *testing.T) {
	s := NewStore()
	a := mustAdd(t, s, Fault{Kind: IPMIDrop})
	mustAdd(t, s, Fault{Kind: RedfishDrop})
	assert.False(t, s.Delete("99"))
	assert.True(t, s.Delete(a.ID))
	require.Len(t, s.List(), 1)
	s.Clear()
	assert.Empty(t, s.List())
}

func TestNilStore_AppliesNothing(t *testing.T) {
	var s *Store
	assert.Equal(t, IPMIAction{}, s.IPMI(IPMIRequest{}))
	assert.False(t, s.IgnoreGraceful("GracefulShutdown"))
	called := false
	require.NoError(t, s.Reset("On", func(string) error { called = true; return nil }))
	assert.True(t, called)
}

func TestMatchPath(t *testing.T) {
	assert.True(t, matchPath("", "/redfish/v1"))
	assert.True(t, matchPath("/redfish/v1/Systems", "/redfish/v1/Systems/1"))
	assert.False(t, matchPath("/redfish/v1/Managers", "/redfish/v1/Systems/1"))
	assert.True(t, matchPath("/redfish/v1/*/1", "/redfish/v1/Systems/1/"))
	assert.False(t, matchPath("/redfish/v1/*/1", "/redfish/v1/Systems/1/Processors"))
}

func TestMatchIPMI(t *testing.T) {
	chassisStatus := IPMIRequest{HasCommand: true, NetFn: 0x00, Cmd: 0x01}
	authCaps := IPMIRequest{HasCommand: true, NetFn: 0x06, Cmd: 0x38, Session: true}
	rakp := IPMIRequest{Session: true}

	all := Fault{Kind: IPMIDrop}
	assert.True(t, all.matchIPMI(chassisStatus))
	assert.False(t, all.matchIPMI(authCaps))
	assert.False(t, all.matchIPMI(rakp))

	withSession := Fault{Kind: IPMIDrop, IncludeSession: true}
	assert.True(t, withSession.matchIPMI(authCaps))
	assert.True(t, withSession.matchIPMI(rakp))

	chassis := Fault{Kind: IPMIDrop, NetFn: bytePtr(0x00), IncludeSession: true}
	assert.True(t, chassis.matchIPMI(chassisStatus))
	assert.False(t, chassis.matchIPMI(authCaps))
	assert.False(t, chassis.matchIPMI(rakp))

	cmd := Fault{Kind: IPMIDrop, NetFn: bytePtr(0x00), Cmd: bytePtr(0x02)}
	assert.False(t, cmd.matchIPMI(chassisStatus))
}

func TestIPMI_Actions(t *testing.T) {
	s := NewStore()
	req := IPMIRequest{HasCommand: true, NetFn: 0x00, Cmd: 0x01}

	mustAdd(t, s, Fault{Kind: IPMIError, CompletionCode: bytePtr(0xC0), Count: intPtr(1)})
	assert.Equal(t, IPMIAction{Error: true, CompletionCode: 0xC0}, s.IPMI(req))

	mustAdd(t, s, Fault{Kind: IPMIDelay, DelayMs: 250, Count: intPtr(1)})
	assert.Equal(t, IPMIAction{Delay: 250 * time.Millisecond}, s.IPMI(req))

	// ipmi-error does not apply to session setup messages (no command)
	mustAdd(t, s, Fault{Kind: IPMIError, IncludeSession: true, Count: intPtr(1)})
	assert.Equal(t, IPMIAction{}, s.IPMI(IPMIRequest{Session: true}))
	s.Clear()

	// bmc-unavailable drops everything, session setup included
	mustAdd(t, s, Fault{Kind: BMCUnavailable})
	assert.True(t, s.IPMI(IPMIRequest{Session: true}).Drop)
	assert.True(t, s.IPMI(req).Drop)
}

func TestReset_PowerActionFail(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, Fault{Kind: PowerActionFail, Action: "ForceOff"})
	var called []string
	reset := func(a string) error { called = append(called, a); return nil }

	err := s.Reset("ForceOff", reset)
	var pe *PowerActionError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, uint8(0xFF), pe.CompletionCode)
	assert.Equal(t, "ForceOff", pe.Action)

	// Other actions are not affected
	require.NoError(t, s.Reset("On", reset))
	assert.Equal(t, []string{"On"}, called)
}

func TestIgnoreGraceful(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, Fault{Kind: GracefulIgnored, Action: "GracefulRestart"})
	assert.False(t, s.IgnoreGraceful("GracefulShutdown"))
	assert.True(t, s.IgnoreGraceful("GracefulRestart"))
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
}

func TestMiddleware_Error(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, Fault{Kind: RedfishError, Status: 503, Method: "post", Path: "/redfish/v1/Systems"})
	srv := httptest.NewServer(s.Middleware(okHandler()))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 503, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"error"`)
	assert.Contains(t, string(body), `"@Message.ExtendedInfo"`)

	// GET and other paths are not affected
	resp2, err := http.Get(srv.URL + "/redfish/v1/Systems/1")
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, 200, resp2.StatusCode)
	resp3, err := http.Post(srv.URL+"/redfish/v1/Managers/1", "application/json", nil)
	require.NoError(t, err)
	resp3.Body.Close()
	assert.Equal(t, 200, resp3.StatusCode)
}

func TestMiddleware_Delay(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, Fault{Kind: RedfishDelay, DelayMs: 200, Count: intPtr(1)})
	srv := httptest.NewServer(s.Middleware(okHandler()))
	defer srv.Close()

	start := time.Now()
	resp, err := http.Get(srv.URL + "/redfish/v1")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond)
}

func TestMiddleware_Drop(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, Fault{Kind: RedfishDrop, Count: intPtr(1)})
	srv := httptest.NewServer(s.Middleware(okHandler()))
	defer srv.Close()

	_, err := http.Get(srv.URL + "/redfish/v1")
	assert.Error(t, err)
	// count 1: back to normal
	resp, err := http.Get(srv.URL + "/redfish/v1")
	require.NoError(t, err)
	resp.Body.Close()
}

func TestMiddleware_Drop_TLS(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, Fault{Kind: RedfishDrop, Count: intPtr(1)})
	srv := httptest.NewTLSServer(s.Middleware(okHandler()))
	defer srv.Close()

	_, err := srv.Client().Get(srv.URL + "/redfish/v1")
	assert.Error(t, err)
}

func TestMiddleware_Hang_ClientGivesUp(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, Fault{Kind: RedfishHang})
	srv := httptest.NewServer(s.Middleware(okHandler()))
	defer srv.Close()

	client := &http.Client{Timeout: 200 * time.Millisecond}
	start := time.Now()
	_, err := client.Get(srv.URL + "/redfish/v1")
	assert.Error(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond)
}

func TestMiddleware_Hang_ServerClosesAfterHangSeconds(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, Fault{Kind: RedfishHang, HangSeconds: 1})
	srv := httptest.NewServer(s.Middleware(okHandler()))
	defer srv.Close()

	start := time.Now()
	_, err := http.Get(srv.URL + "/redfish/v1")
	assert.Error(t, err)
	assert.GreaterOrEqual(t, time.Since(start), time.Second)
}

func TestMiddleware_Hang_EndsWhenDeleted(t *testing.T) {
	s := NewStore()
	f := mustAdd(t, s, Fault{Kind: RedfishHang})
	srv := httptest.NewServer(s.Middleware(okHandler()))
	defer srv.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	var err error
	go func() {
		defer wg.Done()
		_, err = http.Get(srv.URL + "/redfish/v1")
	}()
	time.Sleep(100 * time.Millisecond)
	require.True(t, s.Delete(f.ID))
	wg.Wait()
	assert.Error(t, err)

	resp, err := http.Get(srv.URL + "/redfish/v1")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
}

func TestMiddleware_BMCUnavailable_ForDuration(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, Fault{Kind: BMCUnavailable, DurationSeconds: floatPtr(0.3)})
	srv := httptest.NewServer(s.Middleware(okHandler()))
	defer srv.Close()

	start := time.Now()
	_, err := http.Get(srv.URL + "/redfish/v1")
	assert.Error(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 250*time.Millisecond)

	resp, err := http.Get(srv.URL + "/redfish/v1")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
}
