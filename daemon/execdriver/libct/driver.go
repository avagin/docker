package libct

import (
	"encoding/json"
	"fmt"
	libct "github.com/avagin/libct/go"
	"github.com/dotcloud/docker/daemon/execdriver"
	"github.com/dotcloud/docker/pkg/cgroups"
	"github.com/dotcloud/docker/utils"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"strconv"
	"syscall"
)

const DriverName = "libct"

func init() {
	execdriver.RegisterInitFunc(DriverName, func(args *execdriver.InitArgs) error {
		if err := setupEnv(args); err != nil {
			return err
		}

		if err := setupHostname(args); err != nil {
			return err
		}

		if err := setupNetworking(args); err != nil {
			return err
		}

		if err := setupCapabilities(args); err != nil {
			return err
		}

		if err := setupWorkingDirectory(args); err != nil {
			return err
		}

		if err := changeUser(args); err != nil {
			return err
		}

		path, err := exec.LookPath(args.Args[0])
		if err != nil {
			log.Printf("Unable to locate %v", args.Args[0])
			os.Exit(127)
		}
		if err := syscall.Exec(path, args.Args, os.Environ()); err != nil {
			return fmt.Errorf("dockerinit unable to execute %s - %s", path, err)
		}
		panic("Unreachable")
	})
}

type driver struct {
	root       string // root path for the driver to use
	apparmor   bool
	sharedRoot bool
	session	   *libct.Session
}

func NewDriver(root string, apparmor bool) (*driver, error) {
	s, err := libct.OpenSession()
	if err != nil {
		return nil, err
	}

	return &driver{
		apparmor:   apparmor,
		root:       root,
		sharedRoot: rootIsShared(),
		session: s,
	}, nil
}

func (d *driver) Name() string {
	return fmt.Sprintf("%s", DriverName)
}

func (d *driver) generateEnvConfig(c *execdriver.Command) error {
	data, err := json.Marshal(c.Env)
	if err != nil {
		return err
	}
	p := path.Join(d.root, "containers", c.ID, "config.env")
	c.Mounts = append(c.Mounts, execdriver.Mount{p, "/.dockerenv", false, true})

	return ioutil.WriteFile(p, data, 0600)
}

func (d *driver) Run(c *execdriver.Command, pipes *execdriver.Pipes, startCallback execdriver.StartCallback) (int, error) {
	if err := execdriver.SetTerminal(c, pipes); err != nil {
		return -1, err
	}
	if err := d.generateEnvConfig(c); err != nil {
		return -1, err
	}
	ct, err := d.session.CreateCt(c.ID)
	if err != nil {
		return -1, err
	}

	for _, m := range c.Mounts {
		fmt.Println("%s %s", m.Source, m.Destination)
		ct.AddMount(m.Source, m.Destination) // FIXME
	}
	if (c.Tty) {
		fmt.Println("%s", c.Console)
		ct.AddMount(c.Console, "/dev/console")
	}

	err = ct.SetOption(1);
	if err != nil {
		return -1, err
	}

	err = ct.SetFsRoot(c.Rootfs);
	if err != nil {
		return -1, err
	}

	err = ct.SetFsRoot(c.Rootfs);
	if err != nil {
		return -1, err
	}

	err = ct.SetNsMask(syscall.CLONE_NEWNS | syscall.CLONE_NEWPID);
	if err != nil {
		return -1, err
	}

	params := []string{
		c.InitPath,
		"-driver",
		DriverName,
	}

//	if c.Network.Interface != nil {
//		params = append(params,
//			"-g", c.Network.Interface.Gateway,
//			"-i", fmt.Sprintf("%s/%d", c.Network.Interface.IPAddress, c.Network.Interface.IPPrefixLen),
//		)
//	}
//	params = append(params,
//		"-mtu", strconv.Itoa(c.Network.Mtu),
//	)

	if c.User != "" {
		params = append(params, "-u", c.User)
	}

	if c.Privileged {
		if d.apparmor {
			params[0] = path.Join(d.root, "lxc-start-unconfined")

		}
		params = append(params, "-privileged")
	}

	if c.WorkingDir != "" {
		params = append(params, "-w", c.WorkingDir)
	}

	params = append(params, "--", c.Entrypoint)
	params = append(params, c.Arguments...)

	if d.sharedRoot {
		// lxc-start really needs / to be non-shared, or all kinds of stuff break
		// when lxc-start unmount things and those unmounts propagate to the main
		// mount namespace.
		// What we really want is to clone into a new namespace and then
		// mount / MS_REC|MS_SLAVE, but since we can't really clone or fork
		// without exec in go we have to do this horrible shell hack...
		shellString :=
			"mount --make-rslave /; exec " +
				utils.ShellQuoteArguments(params)

		params = []string{
			"unshare", "-m", "--", "/bin/sh", "-c", shellString,
		}
	}

	var (
		name = params[0]
		arg  = params[1:]
	)
	aname, err := exec.LookPath(name)
	if err != nil {
		aname = name
	}
	c.Path = aname
	c.Args = append([]string{name}, arg...)

	env := make([]string, 0)

	pid, err := ct.Run(aname, c.Args, env, nil)
	if err != nil {
		return -1, err
	}

	c.ContainerPid = int(pid)

	if startCallback != nil {
		startCallback(c)
	}

	waitErr := ct.Wait()

	return 0, waitErr // FIXME
}

// TODO: This can be moved to the mountinfo reader in the mount pkg
func rootIsShared() bool {
	if data, err := ioutil.ReadFile("/proc/self/mountinfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			cols := strings.Split(line, " ")
			if len(cols) >= 6 && cols[4] == "/" {
				return strings.HasPrefix(cols[6], "shared")
			}
		}
	}

	// No idea, probably safe to assume so
	return true
}

func (d *driver) GetPidsForContainer(id string) ([]int, error) {
	pids := []int{}

	// cpu is chosen because it is the only non optional subsystem in cgroups
	subsystem := "cpu"
	cgroupRoot, err := cgroups.FindCgroupMountpoint(subsystem)
	if err != nil {
		return pids, err
	}

	cgroupDir, err := cgroups.GetThisCgroupDir(subsystem)
	if err != nil {
		return pids, err
	}

	filename := filepath.Join(cgroupRoot, cgroupDir, id, "tasks")
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		// With more recent lxc versions use, cgroup will be in lxc/
		filename = filepath.Join(cgroupRoot, cgroupDir, id, "tasks")
	}

	output, err := ioutil.ReadFile(filename)
	if err != nil {
		return pids, err
	}
	for _, p := range strings.Split(string(output), "\n") {
		if len(p) == 0 {
			continue
		}
		pid, err := strconv.Atoi(p)
		if err != nil {
			return pids, fmt.Errorf("Invalid pid '%s': %s", p, err)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

type info struct {
	ID     string
	driver *driver
	state  int
}

const (
        CT_ERROR        = -1
        CT_STOPPED      = 0
        CT_RUNNING      = 1
)


func (i *info) IsRunning() bool {
	return i.state == CT_RUNNING
}

func (d *driver) Info(id string) execdriver.Info {
	state := CT_ERROR

	ct, err := d.session.OpenCt(id)
	if err == nil {
		state, err = ct.State()
	}

	return &info{
		ID:     id,
		driver: d,
		state:	state,
	}
}

func (d *driver) Kill(c *execdriver.Command, sig int) error {
	ct, err := d.session.OpenCt(c.ID)
	if err != nil {
		return err
	}
	return ct.Kill()
}

func (d *driver) Terminate(c *execdriver.Command) error {
	ct, err := d.session.OpenCt(c.ID)
	if err != nil {
		return err
	}
	return ct.Kill()
}
