package redfish

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/fault"
	"github.com/tjst-t/qemu-bmc/internal/firmware"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func newUpdateServer(t *testing.T) (*Server, *firmware.Store) {
	t.Helper()
	fw := firmware.NewStore(filepath.Join(t.TempDir(), "firmware.json"), "1.0.0", firmware.DefaultBIOSVersion)
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	srv.SetFirmware(fw)
	srv.SetFirmwareUpdateDuration(20 * time.Millisecond)
	return srv, fw
}

// imageServer serves firmware images: the path is the content.
func imageServer(t *testing.T) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(strings.ReplaceAll(strings.TrimPrefix(r.URL.Path, "/"), "_", " ") + "\npayload"))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// multipartBody builds a multipart push with the given UpdateParameters
// ("" = none) and UpdateFile content.
func multipartBody(t *testing.T, params, file string) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if params != "" {
		require.NoError(t, mw.WriteField("UpdateParameters", params))
	}
	fw, err := mw.CreateFormFile("UpdateFile", "fw.img")
	require.NoError(t, err)
	fw.Write([]byte(file))
	require.NoError(t, mw.Close())
	return &buf, mw.FormDataContentType()
}

func push(srv *Server, params, file string, t *testing.T) *httptest.ResponseRecorder {
	body, ctype := multipartBody(t, params, file)
	req := httptest.NewRequest("POST", "/redfish/v1/UpdateService/upload", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// waitTask polls the task monitor until it answers 200 and returns the Task.
func waitTask(t *testing.T, srv *Server, monitor string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w := serve(srv, "GET", monitor, "")
		if w.Code == http.StatusOK {
			return decode(t, w.Body.Bytes())
		}
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s did not finish", monitor)
	return nil
}

func messageIDs(task map[string]any) []string {
	var ids []string
	for _, m := range task["Messages"].([]any) {
		ids = append(ids, m.(map[string]any)["MessageId"].(string))
	}
	return ids
}

func TestUpdateService_NotAvailable(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	root := decode(t, serve(srv, "GET", "/redfish/v1", "").Body.Bytes())
	assert.NotContains(t, root, "UpdateService")
	assert.Equal(t, map[string]any{"@odata.id": "/redfish/v1/TaskService"}, root["Tasks"])
	assert.Equal(t, http.StatusNotFound, serve(srv, "GET", "/redfish/v1/UpdateService", "").Code)
	system := decode(t, serve(srv, "GET", "/redfish/v1/Systems/1", "").Body.Bytes())
	assert.NotContains(t, system, "BiosVersion")
}

func TestGetUpdateService(t *testing.T) {
	srv, _ := newUpdateServer(t)
	root := decode(t, serve(srv, "GET", "/redfish/v1", "").Body.Bytes())
	assert.Equal(t, map[string]any{"@odata.id": "/redfish/v1/UpdateService"}, root["UpdateService"])

	for _, p := range []string{"/redfish/v1/UpdateService", "/redfish/v1/UpdateService/"} {
		w := serve(srv, "GET", p, "")
		require.Equal(t, http.StatusOK, w.Code)
		us := decode(t, w.Body.Bytes())
		assert.Equal(t, "/redfish/v1/UpdateService", us["@odata.id"])
		assert.Equal(t, true, us["ServiceEnabled"])
		assert.Equal(t, map[string]any{"@odata.id": "/redfish/v1/UpdateService/FirmwareInventory"}, us["FirmwareInventory"])
		assert.Equal(t, "/redfish/v1/UpdateService/upload", us["MultipartHttpPushUri"])
		action := us["Actions"].(map[string]any)["#UpdateService.SimpleUpdate"].(map[string]any)
		assert.Equal(t, "/redfish/v1/UpdateService/Actions/UpdateService.SimpleUpdate", action["target"])
	}

	col := decode(t, serve(srv, "GET", "/redfish/v1/UpdateService/FirmwareInventory", "").Body.Bytes())
	assert.EqualValues(t, 2, col["Members@odata.count"])
	assert.Equal(t, []any{
		map[string]any{"@odata.id": "/redfish/v1/UpdateService/FirmwareInventory/BMC"},
		map[string]any{"@odata.id": "/redfish/v1/UpdateService/FirmwareInventory/BIOS"},
	}, col["Members"])

	bmc := decode(t, serve(srv, "GET", "/redfish/v1/UpdateService/FirmwareInventory/BMC", "").Body.Bytes())
	assert.Equal(t, "BMC", bmc["Id"])
	assert.Equal(t, "1.0.0", bmc["Version"])
	assert.Equal(t, true, bmc["Updateable"])
	assert.Equal(t, map[string]any{"State": "Enabled", "Health": "OK"}, bmc["Status"])
	assert.Equal(t, []any{map[string]any{"@odata.id": "/redfish/v1/Managers/1"}}, bmc["RelatedItem"])
	bios := decode(t, serve(srv, "GET", "/redfish/v1/UpdateService/FirmwareInventory/BIOS", "").Body.Bytes())
	assert.Equal(t, "QBMC-BIOS-1.0.0", bios["Version"])
	assert.Equal(t, []any{map[string]any{"@odata.id": "/redfish/v1/Systems/1"}}, bios["RelatedItem"])
	assert.Equal(t, http.StatusNotFound, serve(srv, "GET", "/redfish/v1/UpdateService/FirmwareInventory/NIC", "").Code)

	// The same versions on Managers/1 and Systems/1
	mgr := decode(t, serve(srv, "GET", "/redfish/v1/Managers/1", "").Body.Bytes())
	assert.Equal(t, "1.0.0", mgr["FirmwareVersion"])
	system := decode(t, serve(srv, "GET", "/redfish/v1/Systems/1", "").Body.Bytes())
	assert.Equal(t, "QBMC-BIOS-1.0.0", system["BiosVersion"])
}

func TestSimpleUpdate_BMC(t *testing.T) {
	srv, fw := newUpdateServer(t)
	img := imageServer(t)

	w := serve(srv, "POST", "/redfish/v1/UpdateService/Actions/UpdateService.SimpleUpdate",
		`{"ImageURI":"`+img.URL+`/QBMC-FW_BMC_2.0.0","TransferProtocol":"HTTP"}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	monitor := w.Header().Get("Location")
	assert.Equal(t, "/redfish/v1/TaskService/TaskMonitors/1", monitor)
	accepted := decode(t, w.Body.Bytes())
	assert.Equal(t, "/redfish/v1/TaskService/Tasks/1", accepted["@odata.id"])
	assert.Equal(t, monitor, accepted["TaskMonitor"])

	task := waitTask(t, srv, monitor)
	assert.Equal(t, "Completed", task["TaskState"])
	assert.Equal(t, "OK", task["TaskStatus"])
	assert.EqualValues(t, 100, task["PercentComplete"])
	assert.NotEmpty(t, task["EndTime"])
	assert.Equal(t, []string{"Update.1.0.TargetDetermined", "Update.1.0.InstallingOnComponent", "Update.1.0.UpdateSuccessful"}, messageIDs(task))
	assert.Equal(t, task, decode(t, serve(srv, "GET", "/redfish/v1/TaskService/Tasks/1", "").Body.Bytes()))

	assert.Equal(t, "2.0.0", fw.Version("BMC"))
	mgr := decode(t, serve(srv, "GET", "/redfish/v1/Managers/1", "").Body.Bytes())
	assert.Equal(t, "2.0.0", mgr["FirmwareVersion"])
	col := decode(t, serve(srv, "GET", "/redfish/v1/TaskService/Tasks", "").Body.Bytes())
	assert.EqualValues(t, 1, col["Members@odata.count"])
}

func TestSimpleUpdate_BIOSPendingUntilBoot(t *testing.T) {
	srv, fw := newUpdateServer(t)
	img := imageServer(t)

	// Targets given, TransferProtocol from the scheme-less URI
	w := serve(srv, "POST", "/redfish/v1/UpdateService/Actions/UpdateService.SimpleUpdate",
		`{"ImageURI":"`+strings.TrimPrefix(img.URL, "http://")+`/QBMC-FW_BIOS_2.0.0","TransferProtocol":"HTTP",
		  "Targets":["/redfish/v1/UpdateService/FirmwareInventory/BIOS"]}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	task := waitTask(t, srv, w.Header().Get("Location"))
	assert.Equal(t, "Completed", task["TaskState"])
	ids := messageIDs(task)
	assert.Equal(t, "Update.1.0.AwaitToActivate", ids[len(ids)-1])
	last := task["Messages"].([]any)[len(ids)-1].(map[string]any)
	assert.Contains(t, last["Resolution"], "next system boot")

	assert.Equal(t, "QBMC-BIOS-1.0.0", fw.Version("BIOS"))
	bios := decode(t, serve(srv, "GET", "/redfish/v1/UpdateService/FirmwareInventory/BIOS", "").Body.Bytes())
	assert.Equal(t, "QBMC-BIOS-1.0.0", bios["Version"])
	assert.Equal(t, "StandbyOffline", bios["Status"].(map[string]any)["State"])
	assert.Contains(t, bios["Description"], "2.0.0")

	fw.Apply() // the machine boots
	bios = decode(t, serve(srv, "GET", "/redfish/v1/UpdateService/FirmwareInventory/BIOS", "").Body.Bytes())
	assert.Equal(t, "2.0.0", bios["Version"])
	assert.Equal(t, "Enabled", bios["Status"].(map[string]any)["State"])
	system := decode(t, serve(srv, "GET", "/redfish/v1/Systems/1", "").Body.Bytes())
	assert.Equal(t, "2.0.0", system["BiosVersion"])
}

func TestSimpleUpdate_Rejected(t *testing.T) {
	srv, fw := newUpdateServer(t)
	img := imageServer(t)
	path := "/redfish/v1/UpdateService/Actions/UpdateService.SimpleUpdate"

	for _, tc := range []struct {
		body, code string
	}{
		{`{}`, "ActionParameterMissing"},
		{`{"ImageURI":"ftp://x/img"}`, "ActionParameterNotSupported"},
		{`{"ImageURI":"` + img.URL + `/x","TransferProtocol":"NFS"}`, "ActionParameterNotSupported"},
		{`{"ImageURI":"host/img"}`, "ActionParameterNotSupported"},
		{`{"ImageURI":"` + img.URL + `/QBMC-FW_BMC_2","Targets":["/redfish/v1/Chassis/1"]}`, "ActionParameterValueNotInList"},
		{`{"ImageURI":"` + img.URL + `/missing"}`, "Base.1.8.GeneralError"},
		// Broken images
		{`{"ImageURI":"` + img.URL + `/hello"}`, "Base.1.8.GeneralError"},
		{`{"ImageURI":"` + img.URL + `/QBMC-FW_BIOS"}`, "Base.1.8.GeneralError"},
		// Not the image of the Targets
		{`{"ImageURI":"` + img.URL + `/QBMC-FW_BMC_2","Targets":["/redfish/v1/UpdateService/FirmwareInventory/BIOS"]}`, "Base.1.8.GeneralError"},
	} {
		w := serve(srv, "POST", path, tc.body)
		require.Equal(t, http.StatusBadRequest, w.Code, tc.body)
		body := decode(t, w.Body.Bytes())
		assert.Equal(t, tc.code, body["error"].(map[string]any)["code"], tc.body)
	}
	w := serve(srv, "POST", path, `{"ImageURI":"`+img.URL+`/hello"}`)
	info := decode(t, w.Body.Bytes())["error"].(map[string]any)["@Message.ExtendedInfo"].([]any)
	assert.Equal(t, "Update.1.0.VerificationFailed", info[0].(map[string]any)["MessageId"])

	assert.Empty(t, srv.tasks.ids(), "no task for a rejected image")
	assert.Equal(t, "1.0.0", fw.Version("BMC"))
}

func TestMultipartPush(t *testing.T) {
	srv, fw := newUpdateServer(t)

	w := push(srv, `{"Targets":["/redfish/v1/UpdateService/FirmwareInventory/BMC"],"@Redfish.OperationApplyTime":"Immediate"}`,
		"QBMC-FW BMC 3.1\n\x00\x01binary", t)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	task := waitTask(t, srv, w.Header().Get("Location"))
	assert.Equal(t, "Completed", task["TaskState"])
	assert.Equal(t, "3.1", fw.Version("BMC"))

	// Without UpdateParameters the image decides
	w = push(srv, "", "QBMC-FW BIOS b7\n", t)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	waitTask(t, srv, w.Header().Get("Location"))
	assert.Equal(t, "b7", fw.Pending("BIOS"))
}

func TestMultipartPush_Rejected(t *testing.T) {
	srv, fw := newUpdateServer(t)
	for _, tc := range []struct{ params, file string }{
		{"", "garbage"},
		{"", ""},
		{`{"Targets":["/redfish/v1/UpdateService/FirmwareInventory/BIOS"]}`, "QBMC-FW BMC 2\n"},
		{`{"Targets":["/redfish/v1/Nope"]}`, "QBMC-FW BMC 2\n"},
		{`{"@Redfish.OperationApplyTime":"AtMaintenanceWindowStart"}`, "QBMC-FW BMC 2\n"},
		{`not json`, "QBMC-FW BMC 2\n"},
	} {
		w := push(srv, tc.params, tc.file, t)
		assert.Equal(t, http.StatusBadRequest, w.Code, "%+v: %s", tc, w.Body.String())
	}
	// Not multipart, no UpdateFile
	assert.Equal(t, http.StatusBadRequest, serve(srv, "POST", "/redfish/v1/UpdateService/upload", `{}`).Code)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("UpdateParameters", `{}`)
	mw.Close()
	assert.Equal(t, http.StatusBadRequest, serve(srv, "POST", "/redfish/v1/UpdateService/upload", buf.String(),
		"Content-Type", mw.FormDataContentType()).Code)

	assert.Empty(t, srv.tasks.ids())
	assert.Equal(t, "1.0.0", fw.Version("BMC"))
}

func TestUpdate_Progress(t *testing.T) {
	srv, _ := newUpdateServer(t)
	srv.SetFirmwareUpdateDuration(2 * time.Second)
	w := push(srv, "", "QBMC-FW BMC 2\n", t)
	require.Equal(t, http.StatusAccepted, w.Code)
	monitor := w.Header().Get("Location")

	time.Sleep(500 * time.Millisecond)
	w = serve(srv, "GET", monitor, "")
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, monitor, w.Header().Get("Location"))
	task := decode(t, w.Body.Bytes())
	assert.Equal(t, "Running", task["TaskState"])
	p := task["PercentComplete"].(float64)
	assert.True(t, p > 0 && p < 100, "PercentComplete %v", p)
	assert.NotContains(t, task, "EndTime")

	assert.Equal(t, "Completed", waitTask(t, srv, monitor)["TaskState"])
	assert.Equal(t, http.StatusNotFound, serve(srv, "GET", "/redfish/v1/TaskService/TaskMonitors/99", "").Code)
	assert.Equal(t, http.StatusNotFound, serve(srv, "GET", "/redfish/v1/TaskService/Tasks/99", "").Code)
}

func TestUpdate_Faults(t *testing.T) {
	srv, fw := newUpdateServer(t)
	faults := fault.NewStore()
	srv.SetFirmwareFaults(faults)
	one := 1
	_, err := faults.Add(fault.Fault{Kind: fault.FirmwareImageRejected, Target: "BIOS", Count: &one})
	require.NoError(t, err)
	at := 30
	_, err = faults.Add(fault.Fault{Kind: fault.FirmwareUpdateFail, Target: "BMC", AtPercent: &at, Count: &one})
	require.NoError(t, err)

	// firmware-image-rejected: 400 like a broken image
	w := push(srv, "", "QBMC-FW BIOS b2\n", t)
	require.Equal(t, http.StatusBadRequest, w.Code)
	info := decode(t, w.Body.Bytes())["error"].(map[string]any)["@Message.ExtendedInfo"].([]any)
	assert.Equal(t, "Update.1.0.VerificationFailed", info[0].(map[string]any)["MessageId"])
	assert.Empty(t, fw.Pending("BIOS"))

	// firmware-update-fail: Exception at atPercent, version unchanged
	w = push(srv, "", "QBMC-FW BMC 2\n", t)
	require.Equal(t, http.StatusAccepted, w.Code)
	task := waitTask(t, srv, w.Header().Get("Location"))
	assert.Equal(t, "Exception", task["TaskState"])
	assert.Equal(t, "Critical", task["TaskStatus"])
	assert.EqualValues(t, 30, task["PercentComplete"])
	ids := messageIDs(task)
	assert.Equal(t, "Update.1.0.ApplyFailed", ids[len(ids)-1])
	assert.Equal(t, "1.0.0", fw.Version("BMC"))

	// Both counts used up
	w = push(srv, "", "QBMC-FW BIOS b2\n", t)
	require.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "Completed", waitTask(t, srv, w.Header().Get("Location"))["TaskState"])
	w = push(srv, "", "QBMC-FW BMC 2\n", t)
	require.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "Completed", waitTask(t, srv, w.Header().Get("Location"))["TaskState"])
	assert.Equal(t, "2", fw.Version("BMC"))
}

func TestUpdate_TaskEvents(t *testing.T) {
	srv, fw := newUpdateServer(t)
	faults := fault.NewStore()
	srv.SetFirmwareFaults(faults)
	c, done := srv.events.addSSE(func(event) bool { return true })
	defer done()

	w := push(srv, "", "QBMC-FW BMC 2\n", t)
	require.Equal(t, http.StatusAccepted, w.Code)
	waitTask(t, srv, w.Header().Get("Location"))
	_, err := faults.Add(fault.Fault{Kind: fault.FirmwareUpdateFail})
	require.NoError(t, err)
	w = push(srv, "", "QBMC-FW BIOS 2\n", t)
	require.Equal(t, http.StatusAccepted, w.Code)
	waitTask(t, srv, w.Header().Get("Location"))
	assert.Equal(t, "2", fw.Version("BMC"))

	var got []string
	timeout := time.After(2 * time.Second)
	for len(got) < 5 {
		select {
		case ev := <-c.ch:
			got = append(got, ev.record.MessageID+" "+ev.origin())
		case <-timeout:
			t.Fatalf("events: %v", got)
		}
	}
	assert.Equal(t, []string{
		"TaskEvent.1.0.TaskStarted /redfish/v1/TaskService/Tasks/1",
		"ResourceEvent.1.3.ResourceChanged /redfish/v1/UpdateService/FirmwareInventory/BMC",
		"TaskEvent.1.0.TaskCompletedOK /redfish/v1/TaskService/Tasks/1",
		"TaskEvent.1.0.TaskStarted /redfish/v1/TaskService/Tasks/2",
		"TaskEvent.1.0.TaskAborted /redfish/v1/TaskService/Tasks/2",
	}, got)
}

func TestTaskService(t *testing.T) {
	srv, _ := newUpdateServer(t)
	ts := decode(t, serve(srv, "GET", "/redfish/v1/TaskService", "").Body.Bytes())
	assert.Equal(t, "/redfish/v1/TaskService", ts["@odata.id"])
	assert.Equal(t, true, ts["ServiceEnabled"])
	assert.Equal(t, map[string]any{"@odata.id": "/redfish/v1/TaskService/Tasks"}, ts["Tasks"])
	col := decode(t, serve(srv, "GET", "/redfish/v1/TaskService/Tasks/", "").Body.Bytes())
	assert.Equal(t, []any{}, col["Members"])
}

func TestTaskService_Limit(t *testing.T) {
	ts := newTaskService()
	for i := 0; i < maxTasks; i++ {
		_, err := ts.create("t")
		require.NoError(t, err)
	}
	_, err := ts.create("t")
	assert.ErrorIs(t, err, errTooManyTasks, "all running")

	ts.update("3", func(t *task) { t.state = taskCompleted })
	ts.update("5", func(t *task) { t.state = taskException })
	nt, err := ts.create("t")
	require.NoError(t, err)
	assert.Equal(t, "33", nt.id)
	_, ok := ts.get("3")
	assert.False(t, ok, "the oldest finished task is dropped")
	_, ok = ts.get("5")
	assert.True(t, ok)
	assert.Len(t, ts.ids(), maxTasks)
}

func TestFirmwareInventoryJSON(t *testing.T) {
	// Every resource has @odata.type and @odata.id
	srv, _ := newUpdateServer(t)
	w := push(srv, "", "QBMC-FW BMC 2\n", t)
	waitTask(t, srv, w.Header().Get("Location"))
	for _, p := range []string{"/redfish/v1/UpdateService", "/redfish/v1/UpdateService/FirmwareInventory",
		"/redfish/v1/UpdateService/FirmwareInventory/BMC", "/redfish/v1/TaskService", "/redfish/v1/TaskService/Tasks",
		"/redfish/v1/TaskService/Tasks/1"} {
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(serve(srv, "GET", p, "").Body.Bytes(), &m))
		assert.Contains(t, m, "@odata.type", p)
		assert.Contains(t, m, "@odata.id", p)
	}
}
