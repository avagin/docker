package libct

import (
	"github.com/dotcloud/docker/daemon/execdriver"
	"io"
	"os"
)

type Pipes struct {
	Stdin, Stdout, Stderr *os.File
}

type Terminal interface {
	io.Closer
	SetMaster(*os.File)
	Attach() (*Pipes, error)
	Resize(h, w int) error
}

type TtyTerminal struct {
	execdriver.TtyConsole
	pipes *execdriver.Pipes
}

func (t *TtyTerminal) Attach() (*Pipes, error) {
	go io.Copy(t.pipes.Stdout, t.MasterPty)
	if t.pipes.Stdin != nil {
		go io.Copy(t.MasterPty, t.pipes.Stdin)
	}

	return nil, nil
}

func (t *TtyTerminal) SetMaster(master *os.File) {
	t.MasterPty = master
}

type StdTerminal struct {
	execdriver.StdConsole
	pipes *execdriver.Pipes
}

func (s *StdTerminal) SetMaster(*os.File) {
	// no need to set master on non tty
}

func (s *StdTerminal) Attach() (*Pipes, error) {

	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	if s.pipes.Stdin != nil {
		go func() {
			defer inW.Close()
			io.Copy(inW, s.pipes.Stdin)
		}()
	} else {
		inW.Close()
	}

	go io.Copy(s.pipes.Stdout, outR)
	go io.Copy(s.pipes.Stderr, errR)

	return &Pipes{inR, outW, errW}, nil
}

func getTerminal(c *execdriver.Command, pipes *execdriver.Pipes) Terminal {
	var term Terminal
	if c.Tty {
		term = &TtyTerminal{
			pipes: pipes,
		}
	} else {
		term = &StdTerminal{
			pipes: pipes,
		}
	}
	c.Terminal = term
	return term
}
