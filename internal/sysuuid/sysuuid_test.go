package sysuuid

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_Valid(t *testing.T) {
	u, err := Parse("12345678-9ABC-4def-8123-456789abcdef")
	require.NoError(t, err)
	assert.Equal(t, UUID{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0x4d, 0xef, 0x81, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}, u)
	assert.Equal(t, "12345678-9abc-4def-8123-456789abcdef", u.String())
}

func TestParse_Invalid(t *testing.T) {
	for _, s := range []string{
		"",
		"not-a-uuid",
		"12345678-9abc-4def-8123-456789abcde",   // too short
		"12345678-9abc-4def-8123-456789abcdeff", // too long
		"123456789abc-4def-8123-456789abcdef0",  // hyphen misplaced
		"1234567g-9abc-4def-8123-456789abcdef",  // non-hex
	} {
		_, err := Parse(s)
		assert.Error(t, err, s)
	}
}

func TestDerive_DeterministicAndVersion5(t *testing.T) {
	a := Derive("node1")
	b := Derive("node1")
	c := Derive("node2")
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
	assert.Equal(t, byte(0x50), a[6]&0xf0, "version 5")
	assert.Equal(t, byte(0x80), a[8]&0xc0, "RFC 4122 variant")
}

func TestIPMIGUID_ReversesByteOrder(t *testing.T) {
	u := MustParse("00112233-4455-6677-8899-aabbccddeeff")
	assert.Equal(t, []byte{
		0xff, 0xee, 0xdd, 0xcc, 0xbb, 0xaa, 0x99, 0x88,
		0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0x00,
	}, u.IPMIGUID())
}

func TestResolve_ExplicitWins(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "system-uuid")
	require.NoError(t, os.WriteFile(file, []byte("11111111-1111-4111-8111-111111111111\n"), 0o644))

	u, src, err := Resolve(Options{Explicit: "22222222-2222-4222-8222-222222222222", File: file, Hostname: "h"})
	require.NoError(t, err)
	assert.Equal(t, "22222222-2222-4222-8222-222222222222", u.String())
	assert.Equal(t, SourceExplicit, src)
}

func TestResolve_ExplicitInvalid(t *testing.T) {
	_, _, err := Resolve(Options{Explicit: "bogus"})
	assert.Error(t, err)
}

func TestResolve_FileWinsOverHostname(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "system-uuid")
	require.NoError(t, os.WriteFile(file, []byte("11111111-1111-4111-8111-111111111111\n"), 0o644))

	u, src, err := Resolve(Options{File: file, Hostname: "h"})
	require.NoError(t, err)
	assert.Equal(t, "11111111-1111-4111-8111-111111111111", u.String())
	assert.Equal(t, SourceFile, src)
}

func TestResolve_InvalidFileIsError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "system-uuid")
	require.NoError(t, os.WriteFile(file, []byte("garbage"), 0o644))

	_, _, err := Resolve(Options{File: file, Hostname: "h"})
	assert.Error(t, err)
}

func TestResolve_DerivesFromHostnameAndPersists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "system-uuid")

	u, src, err := Resolve(Options{File: file, Hostname: "node1"})
	require.NoError(t, err)
	assert.Equal(t, Derive("node1"), u)
	assert.Equal(t, SourceHostname, src)

	data, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, u.String()+"\n", string(data))

	// A later start with a different hostname keeps the persisted value.
	u2, src2, err := Resolve(Options{File: file, Hostname: "other"})
	require.NoError(t, err)
	assert.Equal(t, u, u2)
	assert.Equal(t, SourceFile, src2)
}

func TestResolve_UnwritableFileStillDerives(t *testing.T) {
	file := filepath.Join(t.TempDir(), "missing-dir", "system-uuid")
	u, src, err := Resolve(Options{File: file, Hostname: "node1"})
	require.NoError(t, err)
	assert.Equal(t, Derive("node1"), u)
	assert.Equal(t, SourceHostname, src)
}

func TestResolve_NoFile(t *testing.T) {
	u, src, err := Resolve(Options{Hostname: "node1"})
	require.NoError(t, err)
	assert.Equal(t, Derive("node1"), u)
	assert.Equal(t, SourceHostname, src)
}
