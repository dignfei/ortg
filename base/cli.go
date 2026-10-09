package base

import (
	"errors"
	"fmt"
	"os"
)

// Command is one subcommand; Run parses its own flags from args.
type Command struct {
	Name  string
	Usage string
	Run   func(args []string) error
}

// ErrUsage maps to exit code 1; ErrData to exit code 2.
var (
	ErrUsage = errors.New("usage")
	ErrData  = errors.New("data")
)

// Run dispatches argv[0] to a command and maps errors to exit codes.
func Run(cmds []Command, argv []string) int {
	if len(argv) == 0 {
		usage(cmds)
		return 1
	}
	for _, c := range cmds {
		if c.Name != argv[0] {
			continue
		}
		err := c.Run(argv[1:])
		switch {
		case err == nil:
			return 0
		case errors.Is(err, ErrUsage):
			fmt.Fprintln(os.Stderr, "ortg:", err)
			return 1
		default:
			fmt.Fprintln(os.Stderr, "ortg:", err)
			return 2
		}
	}
	fmt.Fprintf(os.Stderr, "ortg: unknown command %q\n", argv[0])
	usage(cmds)
	return 1
}

func usage(cmds []Command) {
	fmt.Fprintln(os.Stderr, "usage: ortg <command> [flags]")
	for _, c := range cmds {
		fmt.Fprintf(os.Stderr, "  %-8s %s\n", c.Name, c.Usage)
	}
}

// Debugf writes to stderr only when ORTG_DEBUG=1.
func Debugf(format string, a ...any) {
	if os.Getenv("ORTG_DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "ortg: "+format+"\n", a...)
	}
}
