package slot

import (
	"context"
	"fmt"
)

// fetchFlags keep a fetch to what is asked for: no tags, no FETCH_HEAD, no maintenance run
// and no submodule.
var fetchFlags = []string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance", "--no-recurse-submodules"}

// stagedRef is where a run's branch waits in the repository until it is checked. It is
// outside refs/heads, so nothing that lists branches shows it.
func stagedRef(ws Workspace) string {
	return "refs/epitrop/slot/" + ws.Branch
}

// stageBranch fetches the run's branch from the workspace into the runner's own ref in the
// repository, checks it and returns its head.
//
// Nothing is run inside the workspace. After the run its git directory is the run's own
// writing, configuration and hooks included, and git warns against running commands in such
// a directory. A fetch serves it through git-upload-pack, which is made for that: it takes
// no dangerous setting and no hook from the repository it serves. The objects that arrive
// are checked (fetch.fsckObjects).
//
// The head has to be a commit: the workspace can serve a ref of the branch's name that names
// a tag, and git would refuse that only when it is brought among the branches. It has to be
// the one the result names, when it names one, and it has to descend from the base: a
// branch that shares no history with its base is not this unit's work. No commit may touch
// the job's home or hand back a bound file (checkBranchContent).
func stageBranch(ctx context.Context, repo string, ws Workspace, claimed string, bound []string) (string, error) {
	args := append([]string{"-c", "fetch.fsckObjects=true"}, fetchFlags...)
	args = append(args, "--", ws.Dir, "+refs/heads/"+ws.Branch+":"+stagedRef(ws))
	if _, err := git(ctx, repo, args...); err != nil {
		return "", fmt.Errorf("fetch %s from the workspace: %w", ws.Branch, err)
	}
	head, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", stagedRef(ws))
	if err != nil {
		return "", fmt.Errorf("read the fetched %s: %w", ws.Branch, err)
	}
	kind, err := git(ctx, repo, "cat-file", "-t", head)
	if err != nil {
		return "", fmt.Errorf("read what the fetched %s names: %w", ws.Branch, err)
	}
	if kind != "commit" {
		return "", fmt.Errorf("the branch %s names a %s, not a commit", ws.Branch, kind)
	}
	if claimed != "" && claimed != head {
		return "", fmt.Errorf("the result names head %s, the branch %s is at %s", claimed, ws.Branch, head)
	}
	if _, err = git(ctx, repo, "merge-base", "--is-ancestor", ws.Base, head); err != nil {
		return "", fmt.Errorf("the branch %s at %s does not descend from its base %s: %w", ws.Branch, head, ws.Base, err)
	}
	if err = checkBranchContent(ctx, repo, ws.Base, head, bound); err != nil {
		return "", fmt.Errorf("the branch %s: %w", ws.Branch, err)
	}
	return head, nil
}

// publishBranch brings the staged branch into the repository's branches. It is a fetch of
// the repository from itself, without force: git refuses it when the repository's branch of
// that name is not an ancestor of the new head, and when that branch is checked out.
func publishBranch(ctx context.Context, repo string, ws Workspace) error {
	args := append(append([]string{}, fetchFlags...), ".", stagedRef(ws)+":refs/heads/"+ws.Branch)
	if _, err := git(ctx, repo, args...); err != nil {
		return fmt.Errorf("%w: the repository does not take %s: its own branch of that name is checked out or holds other commits: %w", ErrResult, ws.Branch, err)
	}
	return nil
}

// dropStaged removes the runner's ref again. Git takes the removal of a ref that was never
// made as done.
func dropStaged(ctx context.Context, repo string, ws Workspace) error {
	if _, err := git(ctx, repo, "update-ref", "-d", stagedRef(ws)); err != nil {
		return fmt.Errorf("slot: remove %s: %w", stagedRef(ws), err)
	}
	return nil
}
