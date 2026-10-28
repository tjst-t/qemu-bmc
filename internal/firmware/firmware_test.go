package firmware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadImage_Valid(t *testing.T) {
	for _, tc := range []struct {
		data            string
		target, version string
	}{
		{"QBMC-FW BIOS 2.0.0\n", "BIOS", "2.0.0"},
		{"QBMC-FW BMC 1.2.3-rc1\r\nbinary\x00\xff data", "BMC", "1.2.3-rc1"},
		{"QBMC-FW BMC v9", "BMC", "v9"}, // no newline
	} {
		img, err := ReadImage(strings.NewReader(tc.data))
		require.NoError(t, err, "%q", tc.data)
		assert.Equal(t, tc.target, img.Target)
		assert.Equal(t, tc.version, img.Version)
		assert.EqualValues(t, len(tc.data), img.Size)
	}
}

func TestReadImage_Invalid(t *testing.T) {
	for _, data := range []string{
		"",
		"\n",
		"hello world\n",
		"QBMC-FW\n",
		"QBMC-FW BIOS\n",
		"QBMC-FW BIOS \n",
		"QBMC-FW NIC 1.0\n",
		"QBMC-FW bios 1.0\n",
		"QBMC-FW BIOS 1.0 extra\n",
		"QBMC-FW  BIOS 1.0\n",
		"QBMC-FW BIOS " + strings.Repeat("9", 65) + "\n",
		"QBMC-FW BIOS 1.\x01\n",
		"QBMC-FW BIOS " + strings.Repeat("x", 300),
		" QBMC-FW BIOS 1.0\n",
	} {
		_, err := ReadImage(strings.NewReader(data))
		assert.ErrorIs(t, err, ErrInvalidImage, "%q", data)
	}
}

func TestDownload(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); r.URL.Path == "/auth" && (u != "u" || p != "p") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/bad" {
			w.Write([]byte("not an image"))
			return
		}
		w.Write([]byte("QBMC-FW BMC 3.0\nrest"))
	}))
	defer srv.Close()
	opts := DownloadOptions{InsecureTLS: true}

	img, err := Download(context.Background(), opts, srv.URL+"/img", "", "")
	require.NoError(t, err)
	assert.Equal(t, Image{Target: "BMC", Version: "3.0", Size: 20}, img)

	_, err = Download(context.Background(), opts, srv.URL+"/auth", "", "")
	assert.ErrorContains(t, err, "401")
	_, err = Download(context.Background(), opts, srv.URL+"/auth", "u", "p")
	assert.NoError(t, err)

	_, err = Download(context.Background(), opts, srv.URL+"/bad", "", "")
	assert.ErrorIs(t, err, ErrInvalidImage)

	// Certificates are verified unless InsecureTLS
	_, err = Download(context.Background(), DownloadOptions{}, srv.URL+"/img", "", "")
	assert.Error(t, err)
	assert.False(t, errors.Is(err, ErrInvalidImage))
}

func TestStore_Defaults(t *testing.T) {
	s := NewStore("", "1.0.0", DefaultBIOSVersion)
	assert.Equal(t, "1.0.0", s.Version(TargetBMC))
	assert.Equal(t, "QBMC-BIOS-1.0.0", s.Version(TargetBIOS))
	assert.Empty(t, s.Pending(TargetBIOS))
	assert.False(t, s.Apply())
}

func TestStore_InstallBMCRightAway(t *testing.T) {
	s := NewStore("", "1.0.0", "b1")
	var changed []string
	s.OnChange(func(target string) { changed = append(changed, target) })

	pending, err := s.Install(TargetBMC, "2.0.0")
	require.NoError(t, err)
	assert.False(t, pending)
	assert.Equal(t, "2.0.0", s.Version(TargetBMC))
	assert.Empty(t, s.Pending(TargetBMC))
	assert.Equal(t, []string{"BMC"}, changed)

	_, err = s.Install("NIC", "1")
	assert.Error(t, err)
}

func TestStore_BIOSPendingUntilBoot(t *testing.T) {
	s := NewStore("", "1.0.0", "b1")
	var changed []string
	s.OnChange(func(target string) { changed = append(changed, target) })

	pending, err := s.Install(TargetBIOS, "b2")
	require.NoError(t, err)
	assert.True(t, pending)
	assert.Equal(t, "b1", s.Version(TargetBIOS))
	assert.Equal(t, "b2", s.Pending(TargetBIOS))

	// A newer update replaces the pending one
	_, err = s.Install(TargetBIOS, "b3")
	require.NoError(t, err)
	assert.Equal(t, "b3", s.Pending(TargetBIOS))

	assert.True(t, s.Apply())
	assert.Equal(t, "b3", s.Version(TargetBIOS))
	assert.Empty(t, s.Pending(TargetBIOS))
	assert.False(t, s.Apply())
	assert.Equal(t, []string{"BIOS", "BIOS", "BIOS"}, changed)
}

func TestStore_Persistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "firmware.json")
	s := NewStore(path, "1.0.0", "b1")
	_, err := s.Install(TargetBMC, "2.0.0")
	require.NoError(t, err)
	_, err = s.Install(TargetBIOS, "b2")
	require.NoError(t, err)

	// The saved versions win over the configured ones
	s2 := NewStore(path, "9.9.9", "b9")
	assert.Equal(t, "2.0.0", s2.Version(TargetBMC))
	assert.Equal(t, "b9", s2.Version(TargetBIOS))
	assert.Equal(t, "b2", s2.Pending(TargetBIOS))
	s2.Apply()

	s3 := NewStore(path, "9.9.9", "b9")
	assert.Equal(t, "b2", s3.Version(TargetBIOS))
	assert.Empty(t, s3.Pending(TargetBIOS))
}

func TestStore_LoadIgnoresInvalid(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.json")
	require.NoError(t, os.WriteFile(broken, []byte("{"), 0o644))
	s := NewStore(broken, "1", "b1")
	assert.Equal(t, "1", s.Version(TargetBMC))

	odd := filepath.Join(dir, "odd.json")
	require.NoError(t, os.WriteFile(odd, []byte(`{"Versions":{"BMC":"has space","NIC":"1","BIOS":"b5"}}`), 0o644))
	s = NewStore(odd, "1", "b1")
	assert.Equal(t, "1", s.Version(TargetBMC))
	assert.Equal(t, "b5", s.Version(TargetBIOS))
}
