package redfish

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

const (
	taskServicePath  = "/redfish/v1/TaskService"
	tasksPath        = taskServicePath + "/Tasks"
	taskMonitorsPath = taskServicePath + "/TaskMonitors"

	// maxTasks bounds the number of tasks kept (in memory only); the oldest
	// finished task is dropped to make room, and creating one more while
	// all are running fails with 503.
	maxTasks = 32

	// taskEventRegistry is the DMTF TaskEvent registry the task events
	// refer to.
	taskEventRegistry = "TaskEvent.1.0"
)

// TaskState values
const (
	taskNew       = "New"
	taskRunning   = "Running"
	taskCompleted = "Completed"
	taskException = "Exception"
)

// taskEvents are the TaskEvent messages qemu-bmc sends (%1 = task Id).
var taskEvents = map[string]struct {
	severity string
	message  string
}{
	"TaskStarted":     {"OK", "The task with Id '%1' has started."},
	"TaskCompletedOK": {"OK", "The task with Id '%1' has completed."},
	"TaskAborted":     {"Critical", "The task with Id '%1' has completed with errors."},
}

// task is a long-running operation (a firmware update).
type task struct {
	id         string
	name       string
	state      string
	status     string // TaskStatus: OK, Warning, Critical
	percent    int
	messages   []Message
	start, end time.Time
}

func (t *task) finished() bool {
	return t.state == taskCompleted || t.state == taskException
}

// taskService holds the tasks.
type taskService struct {
	mu     sync.Mutex
	tasks  map[string]*task
	nextID int
	now    func() time.Time
}

func newTaskService() *taskService {
	return &taskService{tasks: map[string]*task{}, now: time.Now}
}

var errTooManyTasks = errors.New("too many tasks in progress")

// create adds a New task, dropping the oldest finished task if there are
// maxTasks already.
func (ts *taskService) create(name string, msgs ...Message) (task, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.tasks) >= maxTasks {
		var oldest *task
		for _, t := range ts.tasks {
			if t.finished() && (oldest == nil || taskIDLess(t.id, oldest.id)) {
				oldest = t
			}
		}
		if oldest == nil {
			return task{}, errTooManyTasks
		}
		delete(ts.tasks, oldest.id)
	}
	ts.nextID++
	t := &task{
		id:       strconv.Itoa(ts.nextID),
		name:     name,
		state:    taskNew,
		status:   "OK",
		messages: msgs,
		start:    ts.now(),
	}
	ts.tasks[t.id] = t
	return t.snapshot(), nil
}

func (t *task) snapshot() task {
	c := *t
	c.messages = append([]Message{}, t.messages...)
	return c
}

// update changes task id under the lock and returns its state before and
// the task after; a task entering Completed or Exception gets its EndTime.
func (ts *taskService) update(id string, f func(t *task)) (before string, after task, ok bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.tasks[id]
	if !ok {
		return "", task{}, false
	}
	before = t.state
	wasFinished := t.finished()
	f(t)
	if !wasFinished && t.finished() {
		t.end = ts.now()
	}
	return before, t.snapshot(), true
}

func (ts *taskService) get(id string) (task, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.tasks[id]
	if !ok {
		return task{}, false
	}
	return t.snapshot(), true
}

func (ts *taskService) ids() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ids := make([]string, 0, len(ts.tasks))
	for id := range ts.tasks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return taskIDLess(ids[i], ids[j]) })
	return ids
}

func taskIDLess(a, b string) bool {
	x, _ := strconv.Atoi(a)
	y, _ := strconv.Atoi(b)
	return x < y
}

func taskPath(id string) string        { return tasksPath + "/" + id }
func taskMonitorPath(id string) string { return taskMonitorsPath + "/" + id }

// TaskServiceResource is the Redfish TaskService.
type TaskServiceResource struct {
	ODataType                       string         `json:"@odata.type"`
	ODataID                         string         `json:"@odata.id"`
	ID                              string         `json:"Id"`
	Name                            string         `json:"Name"`
	ServiceEnabled                  bool           `json:"ServiceEnabled"`
	CompletedTaskOverWritePolicy    string         `json:"CompletedTaskOverWritePolicy"`
	LifeCycleEventOnTaskStateChange bool           `json:"LifeCycleEventOnTaskStateChange"`
	DateTime                        string         `json:"DateTime"`
	Status                          ResourceStatus `json:"Status"`
	Tasks                           ODataID        `json:"Tasks"`
}

// TaskResource is a Redfish Task.
type TaskResource struct {
	ODataType       string    `json:"@odata.type"`
	ODataID         string    `json:"@odata.id"`
	ID              string    `json:"Id"`
	Name            string    `json:"Name"`
	TaskState       string    `json:"TaskState"`
	TaskStatus      string    `json:"TaskStatus"`
	PercentComplete int       `json:"PercentComplete"`
	StartTime       string    `json:"StartTime"`
	EndTime         string    `json:"EndTime,omitempty"`
	TaskMonitor     string    `json:"TaskMonitor"`
	Messages        []Message `json:"Messages"`
}

func (t task) resource() TaskResource {
	r := TaskResource{
		ODataType:       "#Task.v1_4_3.Task",
		ODataID:         taskPath(t.id),
		ID:              t.id,
		Name:            t.name,
		TaskState:       t.state,
		TaskStatus:      t.status,
		PercentComplete: t.percent,
		StartTime:       t.start.UTC().Format(time.RFC3339),
		TaskMonitor:     taskMonitorPath(t.id),
		Messages:        t.messages,
	}
	if !t.end.IsZero() {
		r.EndTime = t.end.UTC().Format(time.RFC3339)
	}
	if r.Messages == nil {
		r.Messages = []Message{}
	}
	return r
}

func (s *Server) handleGetTaskService(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, TaskServiceResource{
		ODataType:                       "#TaskService.v1_1_0.TaskService",
		ODataID:                         taskServicePath,
		ID:                              "TaskService",
		Name:                            "Task Service",
		ServiceEnabled:                  true,
		CompletedTaskOverWritePolicy:    "Oldest",
		LifeCycleEventOnTaskStateChange: true,
		DateTime:                        s.tasks.now().UTC().Format(time.RFC3339),
		Status:                          statusOK,
		Tasks:                           ODataID{ODataID: tasksPath},
	})
}

func (s *Server) handleTaskCollection(w http.ResponseWriter, r *http.Request) {
	ids := s.tasks.ids()
	members := make([]ODataID, 0, len(ids))
	for _, id := range ids {
		members = append(members, ODataID{ODataID: taskPath(id)})
	}
	writeJSON(w, http.StatusOK, Collection{
		ODataType:    "#TaskCollection.TaskCollection",
		ODataID:      tasksPath,
		Name:         "Task Collection",
		MembersCount: len(members),
		Members:      members,
	})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tasks.get(mux.Vars(r)["taskid"])
	if !ok {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "task not found")
		return
	}
	writeJSON(w, http.StatusOK, t.resource())
}

// handleTaskMonitor serves a task monitor: 202 with the Task while the task
// runs, then 200 with the Task.
func (s *Server) handleTaskMonitor(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tasks.get(mux.Vars(r)["taskid"])
	if !ok {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "task not found")
		return
	}
	if !t.finished() {
		writeTaskAccepted(w, t)
		return
	}
	writeJSON(w, http.StatusOK, t.resource())
}

// writeTaskAccepted answers 202 Accepted with the task monitor as Location
// and the Task as body.
func writeTaskAccepted(w http.ResponseWriter, t task) {
	w.Header().Set("Location", taskMonitorPath(t.id))
	w.Header().Set("Retry-After", "1")
	writeJSON(w, http.StatusAccepted, t.resource())
}

// updateTask changes a task and sends the TaskEvent of a state change
// (Running → TaskStarted, Completed → TaskCompletedOK, Exception →
// TaskAborted).
func (s *Server) updateTask(id string, f func(t *task)) {
	before, after, ok := s.tasks.update(id, f)
	if !ok || before == after.state {
		return
	}
	switch after.state {
	case taskRunning:
		s.events.publish(taskEvent("TaskStarted", id))
	case taskCompleted:
		s.events.publish(taskEvent("TaskCompletedOK", id))
	case taskException:
		s.events.publish(taskEvent("TaskAborted", id))
	}
}

// taskEvent returns a TaskEvent registry event about task id.
func taskEvent(key, id string) event {
	msg := taskEvents[key]
	return event{
		record: EventRecord{
			EventType:         "StatusChange",
			Message:           strings.ReplaceAll(msg.message, "%1", id),
			MessageArgs:       []string{id},
			MessageID:         taskEventRegistry + "." + key,
			MessageSeverity:   msg.severity,
			OriginOfCondition: &ODataID{ODataID: taskPath(id)},
		},
		resourceType: "Task",
	}
}
