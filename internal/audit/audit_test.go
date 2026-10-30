package audit

import (
	"bytes"
	"log"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEntry_String(t *testing.T) {
	e := New(KindPower, ViaRedfish, "192.0.2.10:51234").SetUser("admin").Set("reset_type", "ForceOff")
	assert.Equal(t, "action: kind=power via=redfish remote=192.0.2.10 user=admin reset_type=ForceOff result=accepted", e.String())

	e.Result(Rejected, "status", "409", "reason", "ActionNotSupported")
	assert.Equal(t, "action: kind=power via=redfish remote=192.0.2.10 user=admin reset_type=ForceOff result=rejected status=409 reason=ActionNotSupported", e.String())
}

func TestEntry_EmptyAndQuotedValues(t *testing.T) {
	e := New(KindVMediaInsert, ViaRedfish, "").Set("image", "http://h/a b.iso").Set("x", `a"b`).Set("y", "k=v")
	assert.Equal(t, `action: kind=vmedia-insert via=redfish remote=- user=- image="http://h/a b.iso" x="a\"b" y="k=v" result=accepted`, e.String())
}

func TestEntry_SetReplaces(t *testing.T) {
	e := New(KindBoot, ViaIPMI, "[2001:db8::1]:623").Set("target", "Pxe").Set("target", "Cd")
	assert.Equal(t, "action: kind=boot via=ipmi remote=2001:db8::1 user=- target=Cd result=accepted", e.String())
}

func TestEntry_LogsOnce(t *testing.T) {
	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	}()

	e := New(KindPower, ViaIPMI, "192.0.2.1:623").Set("reset_type", "On")
	e.Log()
	e.Log()
	assert.Equal(t, "action: kind=power via=ipmi remote=192.0.2.1 user=- reset_type=On result=accepted\n", buf.String())
}

func TestEntry_NilIsNoOp(t *testing.T) {
	var e *Entry
	assert.NotPanics(t, func() {
		e.SetUser("u").Set("k", "v").Result(Fault)
		e.Log()
		assert.False(t, e.HasResult())
		assert.Equal(t, "", e.String())
	})
}
