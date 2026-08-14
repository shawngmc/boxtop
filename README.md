# boxtop 

A tool to measure cgroup-based workspaces (docker, k8s, etc.).

Features:
- Mouse support for column sorting and scrolling via wheel
- Falls back to host constraints if not constrained/in cgroup



## Building

```sh
go mod tidy 
go build -o boxtop .
./boxtop 1    # 1-second refresh
```

## TODOs
- **No raw-mode fallback path.** This should detects a non-interactive
  stdin (piped/redirected) and fall back to a plain timer with no key
  handling. This scaffold always runs through tcell's event loop; add an
  `isatty`-style check up front if you need the non-interactive mode.
- **`golang.org/x/sys/unix` niceties omitted.** `runtimeNumCPU()` uses
  `runtime.NumCPU()` (host total) rather than
  `unix.SchedGetaffinity` (schedulable cores), and `CLK_TCK`/page size
  are hardcoded constants rather than read via `sysconf`. Noted inline
  in `util.go` — swap in `x/sys/unix` if exact parity matters for your
  containers.
- **Show CPU Make/Model and Clockspeed**

## AI Disclosure
Claude Code was used to help write this, including the original python
prototype. This is being used as an entry point into Go, a language
I haven't used much, so feedback is appreciated.
