package procx

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW          = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject   = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject  = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject        = kernel32.NewProc("TerminateJobObject")
	procQueryInformationJobObject = kernel32.NewProc("QueryInformationJobObject")
)

const (
	jobObjectBasicAccountingInformation = 1
	jobObjectExtendedLimitInformation   = 9
	jobObjectLimitKillOnJobClose        = 0x2000
	processSetQuota                     = 0x0100
	processTerminate                    = 0x0001
)

// jobLimits is JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type jobLimits struct {
	perProcessUserTimeLimit int64
	perJobUserTimeLimit     int64
	limitFlags              uint32
	minimumWorkingSetSize   uintptr
	maximumWorkingSetSize   uintptr
	activeProcessLimit      uint32
	affinity                uintptr
	priorityClass           uint32
	schedulingClass         uint32
	io                      [6]uint64
	processMemoryLimit      uintptr
	jobMemoryLimit          uintptr
	peakProcessMemoryUsed   uintptr
	peakJobMemoryUsed       uintptr
}

// jobAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type jobAccounting struct {
	totalUserTime             int64
	totalKernelTime           int64
	thisPeriodTotalUserTime   int64
	thisPeriodTotalKernelTime int64
	totalPageFaultCount       uint32
	totalProcesses            uint32
	activeProcesses           uint32
	totalTerminatedProcesses  uint32
}

// tree is a started process and the job object that holds it and its descendants.
type tree struct {
	job  uintptr
	once sync.Once
}

func (t *tree) stop() {
	t.once.Do(func() {
		procTerminateJobObject.Call(t.job, 1)
		syscall.CloseHandle(syscall.Handle(t.job))
	})
}

// active is how many processes the job holds now.
func (t *tree) active() int {
	var a jobAccounting
	if r, _, _ := procQueryInformationJobObject.Call(t.job, jobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&a)),
		unsafe.Sizeof(a), 0); r == 0 {
		return -1
	}
	return int(a.activeProcesses)
}

func startTree(cmd *exec.Cmd) (func(), error) {
	t, err := newTree(cmd)
	if err != nil {
		return nil, err
	}
	return t.stop, nil
}

func newTree(cmd *exec.Cmd) (*tree, error) {
	job, _, err := procCreateJobObjectW.Call(0, 0)
	if job == 0 {
		return nil, err
	}
	lim := jobLimits{limitFlags: jobObjectLimitKillOnJobClose} // if this process dies, Windows closes the handle and ends the tree
	if r, _, err := procSetInformationJobObject.Call(job, jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&lim)),
		unsafe.Sizeof(lim)); r == 0 {
		syscall.CloseHandle(syscall.Handle(job))
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		syscall.CloseHandle(syscall.Handle(job))
		return nil, err
	}
	h, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(cmd.Process.Pid))
	if err == nil {
		r, _, aerr := procAssignProcessToJobObject.Call(job, uintptr(h))
		syscall.CloseHandle(h)
		if r == 0 {
			err = aerr
		}
	}
	if err != nil {
		_ = cmd.Process.Kill()
		syscall.CloseHandle(syscall.Handle(job))
		return nil, err
	}
	return &tree{job: job}, nil
}

// createNoWindow is CREATE_NO_WINDOW: the child is a console program that runs without a console window.
const createNoWindow = 0x08000000

// Flags are the creation flags Background adds (exported for tests).
const Flags = createNoWindow

// detachedProcess and createNewProcessGroup are DETACHED_PROCESS and CREATE_NEW_PROCESS_GROUP.
const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= detachedProcess | createNewProcessGroup
}

func hide(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
