// Command signoff-packets writes the review workbooks for the human sign-offs tracked in #512: one .xlsx per review, made
// from fixtures already in the repository, with a cover sheet (what to check, the source fixture, the git commit, the
// generation date and the sign-off fields), the inputs, the expected results and, where it helps, the totals to tie out.
//
// From the repository root (the tool is its own Go module, so go -C runs it from its directory; give -out as an absolute
// path, a relative one is taken from tools/signoff-packets):
//
//	go -C tools/signoff-packets run . -out /tmp/packets            # every packet
//	go -C tools/signoff-packets run . -out /tmp/packets -only tax  # one packet (a comma-separated list works too)
//	go -C tools/signoff-packets run . -list                        # the packets and the #512 item each serves
//
// The same commit gives byte-identical workbooks apart from the generation date, which -date sets.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "signoff-packets:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("signoff-packets", flag.ContinueOnError)
	out := fs.String("out", "", "directory the workbooks are written to (created if missing)")
	only := fs.String("only", "", "write only these packets (comma-separated names, see -list)")
	date := fs.String("date", "", "generation date printed on the cover, YYYY-MM-DD (default today)")
	commit := fs.String("commit", "", "git commit printed on the cover (default: git rev-parse HEAD of the repository)")
	repo := fs.String("repo", "", "repository root (default: found from the working directory)")
	list := fs.Bool("list", false, "list the packets and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *list {
		for _, p := range packets {
			_, _ = fmt.Fprintf(stdout, "%-18s %s\n", p.Name, p.Item)
		}
		return nil
	}
	if *out == "" {
		return errors.New("-out is required")
	}
	selected, err := selectPackets(*only)
	if err != nil {
		return err
	}
	root := *repo
	if root == "" {
		if root, err = findRepoRoot(); err != nil {
			return err
		}
	}
	day := *date
	if day == "" {
		day = time.Now().Format(time.DateOnly)
	} else if _, err := time.Parse(time.DateOnly, day); err != nil {
		return fmt.Errorf("-date %q is not YYYY-MM-DD", day)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for _, p := range selected {
		meta := Meta{Commit: *commit, Date: day}
		if meta.Commit == "" {
			meta.Commit = gitCommit(root, p.Sources)
		}
		book, _, err := Generate(p, root, meta)
		if err != nil {
			return fmt.Errorf("%s: %w", p.Name, err)
		}
		path := filepath.Join(*out, p.Name+".xlsx")
		if err := os.WriteFile(path, book, 0o644); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "%s\t%s\n", path, p.Item)
	}
	return nil
}

func selectPackets(only string) ([]Packet, error) {
	if strings.TrimSpace(only) == "" {
		return packets, nil
	}
	byName := map[string]Packet{}
	for _, p := range packets {
		byName[p.Name] = p
	}
	var out []Packet
	for _, name := range strings.Split(only, ",") {
		p, ok := byName[strings.TrimSpace(name)]
		if !ok {
			return nil, fmt.Errorf("no packet %q (see -list)", strings.TrimSpace(name))
		}
		out = append(out, p)
	}
	return out, nil
}

// findRepoRoot walks up from the working directory to the directory holding services/ and platform/go/.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if isDir(filepath.Join(dir, "services")) && isDir(filepath.Join(dir, "platform", "go")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run inside the repository or pass -repo")
		}
		dir = parent
	}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// gitCommit is HEAD of the repository, marked when a source fixture differs from it.
func gitCommit(root string, sources []string) string {
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown (not a git checkout)"
	}
	commit := strings.TrimSpace(string(head))
	status, err := exec.Command("git", append([]string{"-C", root, "status", "--porcelain", "--"}, sources...)...).Output()
	if err == nil && strings.TrimSpace(string(status)) != "" {
		commit += " (source fixtures changed locally)"
	}
	return commit
}
