// Command chat-agent-prune removes old application releases.
//
// It exists for the narrowed maintenance path: an operator pre-registers the exact
// argv in CHAT_SERVICE_ACTIONS and the model can only choose that action, never
// compose its own. Therefore this program must be safe on its own terms — it only
// ever touches /opt/chat-agent/releases, never the release `current` points at, and
// it prints what it would do unless --apply is present.
//
// Old release directories pile up silently: one per deployment, roughly ten
// megabytes each. Ninety of them is most of a gigabyte.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// releasesRoot is fixed rather than configurable: a flag that redirected it would
// turn a narrow maintenance helper into a general-purpose recursive delete.
const releasesRoot = "/opt/chat-agent/releases"

type entry struct {
	Name    string    `json:"name"`
	ModTime time.Time `json:"modified"`
	Current bool      `json:"current"`
	Action  string    `json:"action"`
}

func main() {
	keep := flag.Int("keep", 10, "how many of the newest releases to keep")
	apply := flag.Bool("apply", false, "delete instead of reporting")
	// The host appends this when invoking a maintenance action; accepting and
	// ignoring it keeps the flag parser from refusing the call.
	_ = flag.String("operation-id", "", "host-supplied operation identity")
	flag.Parse()

	if *keep < 1 {
		fail(errors.New("--keep must be at least 1"))
	}
	if err := run(*keep, *apply); err != nil {
		fail(err)
	}
}

func run(keep int, apply bool) error {
	current, err := currentRelease()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(releasesRoot)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", releasesRoot, err)
	}

	releases := make([]entry, 0, len(entries))
	for _, item := range entries {
		if !item.IsDir() {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		releases = append(releases, entry{Name: item.Name(), ModTime: info.ModTime(), Current: item.Name() == current})
	}
	// Newest first, so the first `keep` are the ones to preserve.
	sort.Slice(releases, func(i, j int) bool {
		if releases[i].ModTime.Equal(releases[j].ModTime) {
			return releases[i].Name > releases[j].Name
		}
		return releases[i].ModTime.After(releases[j].ModTime)
	})

	removable := []entry{}
	for index, release := range releases {
		switch {
		case release.Current:
			release.Action = "kept: current"
		case index < keep:
			release.Action = "kept: recent"
		default:
			release.Action = "delete"
			removable = append(removable, release)
		}
	}

	freed := int64(0)
	for index := range removable {
		size, err := dirSize(filepath.Join(releasesRoot, removable[index].Name))
		if err == nil {
			freed += size
		}
	}

	if apply {
		for _, release := range removable {
			path := filepath.Join(releasesRoot, release.Name)
			// Belt and braces: the loop above already excluded current, but a delete
			// this blunt should re-check rather than trust its caller.
			if release.Name == current {
				return fmt.Errorf("refusing to delete the current release %q", release.Name)
			}
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("cannot delete %s: %w", path, err)
			}
		}
	}

	summary := fmt.Sprintf("%d 个版本，保留 %d 个（含当前 %s），可清理 %d 个，约 %.1f MiB",
		len(releases), len(releases)-len(removable), current, len(removable), float64(freed)/(1<<20))
	if !apply && len(removable) > 0 {
		summary += "。这是预演，加 --apply 才会真的删除"
	}
	if apply && len(removable) > 0 {
		summary += fmt.Sprintf("。已删除 %d 个", len(removable))
	}

	body, err := json.MarshalIndent(map[string]any{
		"root":      releasesRoot,
		"current":   current,
		"keep":      keep,
		"applied":   apply,
		"freed_mib": float64(freed) / (1 << 20),
		"releases":  releases,
	}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(summary)
	fmt.Println(string(body))
	return nil
}

// currentRelease returns the directory name that /opt/chat-agent/current resolves to.
func currentRelease() (string, error) {
	target, err := filepath.EvalSymlinks("/opt/chat-agent/current")
	if err != nil {
		return "", fmt.Errorf("cannot resolve current release: %w", err)
	}
	name := filepath.Base(strings.TrimSpace(target))
	if name == "" || name == "/" || name == "." {
		return "", errors.New("current release did not resolve to a directory name")
	}
	return name, nil
}

func dirSize(root string) (int64, error) {
	total := int64(0)
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
