package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/opencontainers/specs"
)

type Process interface {
	io.Closer

	// ID of the process.
	// This is either "init" when it is the container's init process or
	// it is a user provided id for the process similar to the container id
	ID() string
	CloseStdin() error
	Resize(int, int) error
	// ExitFD returns the fd the provides an event when the process exits
	ExitFD() int
	// ExitStatus returns the exit status of the process or an error if it
	// has not exited
	ExitStatus() (int, error)
	// Spec returns the process spec that created the process
	Spec() specs.Process
	// Signal sends the provided signal to the process
	Signal(os.Signal) error
	// Container returns the container that the process belongs to
	Container() Container
	// Stdio of the container
	Stdio() Stdio
	// SystemPid is the pid on the system
	SystemPid() int
}

type processConfig struct {
	id          string
	root        string
	processSpec specs.Process
	spec        *platformSpec
	c           *container
	stdio       Stdio
	exec        bool
	checkpoint  string
}

func newProcess(config *processConfig) (*process, error) {
	p := &process{
		root:      config.root,
		id:        config.id,
		container: config.c,
		spec:      config.processSpec,
		stdio:     config.stdio,
	}
	uid, gid, err := getRootIDs(config.spec)
	if err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(config.root, "process.json"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(ProcessState{
		Process:    config.processSpec,
		Exec:       config.exec,
		Checkpoint: config.checkpoint,
		RootUID:    uid,
		RootGID:    gid,
		Stdin:      config.stdio.Stdin,
		Stdout:     config.stdio.Stdout,
		Stderr:     config.stdio.Stderr,
	}); err != nil {
		return nil, err
	}
	exit, err := getExitPipe(filepath.Join(config.root, ExitFile))
	if err != nil {
		return nil, err
	}
	control, err := getControlPipe(filepath.Join(config.root, ControlFile))
	if err != nil {
		return nil, err
	}
	p.exitPipe = exit
	p.controlPipe = control
	return p, nil
}

func loadProcess(root, id string, c *container, s *ProcessState) (*process, error) {
	p := &process{
		root:      root,
		id:        id,
		container: c,
		spec:      s.Process,
		stdio: Stdio{
			Stdin:  s.Stdin,
			Stdout: s.Stdout,
			Stderr: s.Stderr,
		},
	}
	if _, err := p.getPid(); err != nil {
		return nil, err
	}
	if _, err := p.ExitStatus(); err != nil {
		if err == ErrProcessNotExited {
			exit, err := getExitPipe(filepath.Join(root, ExitFile))
			if err != nil {
				return nil, err
			}
			p.exitPipe = exit
			return p, nil
		}
		return nil, err
	}
	return p, nil
}

type process struct {
	root        string
	id          string
	pid         int
	exitPipe    *os.File
	controlPipe *os.File
	container   *container
	spec        specs.Process
	stdio       Stdio
}

func (p *process) ID() string {
	return p.id
}

func (p *process) Container() Container {
	return p.container
}

func (p *process) SystemPid() int {
	return p.pid
}

// ExitFD returns the fd of the exit pipe
func (p *process) ExitFD() int {
	return int(p.exitPipe.Fd())
}

func (p *process) CloseStdin() error {
	_, err := fmt.Fprintf(p.controlPipe, "%d %d %d\n", 0, 0, 0)
	return err
}

func (p *process) Resize(w, h int) error {
	_, err := fmt.Fprintf(p.controlPipe, "%d %d %d\n", 1, w, h)
	return err
}

func (p *process) ExitStatus() (int, error) {
	data, err := ioutil.ReadFile(filepath.Join(p.root, ExitStatusFile))
	if err != nil {
		if os.IsNotExist(err) {
			return -1, ErrProcessNotExited
		}
		return -1, err
	}
	if len(data) == 0 {
		return -1, ErrProcessNotExited
	}
	return strconv.Atoi(string(data))
}

func (p *process) Spec() specs.Process {
	return p.spec
}

func (p *process) Stdio() Stdio {
	return p.stdio
}

// Close closes any open files and/or resouces on the process
func (p *process) Close() error {
	return p.exitPipe.Close()
}

func (p *process) getPid() (int, error) {
	for i := 0; i < 20; i++ {
		data, err := ioutil.ReadFile(filepath.Join(p.root, "pid"))
		if err != nil {
			if os.IsNotExist(err) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return -1, err
		}
		i, err := strconv.Atoi(string(data))
		if err != nil {
			return -1, err
		}
		p.pid = i
		return i, nil
	}
	return -1, fmt.Errorf("containerd: cannot read pid file")
}